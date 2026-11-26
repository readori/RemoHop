package com.remohop.app

import android.content.Context
import android.hardware.display.DisplayManager
import android.hardware.display.VirtualDisplay
import android.media.MediaCodec
import android.media.MediaCodecInfo
import android.media.MediaFormat
import android.media.projection.MediaProjection
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.HandlerThread
import android.util.DisplayMetrics
import android.view.Display
import android.view.Surface
import com.remohop.app.core.remohopcore.Remohopcore
import com.remohop.app.core.remohopcore.ScreenSource

/**
 * The phone's screen for the core (its ScreenSource, internal/mobile): a
 * MediaProjection mirrored into a VirtualDisplay whose surface is the input
 * of a hardware H.264 MediaCodec encoder. The pixels never pass through
 * the CPU; only the encoded frames do, on their way to the core
 * (Remohopcore.pushScreenFrame), which sends them to the viewers.
 *
 * One VirtualDisplay lives as long as the projection (Android 14 allows
 * one per consent). The encoder is recreated when the picture's size
 * changes (the phone turns, a viewer asks for another size or frame rate)
 * and the display is pointed at the new encoder's surface and resized; the
 * new encoder starts with its SPS and PPS and a key frame, from which the
 * core starts a new stream.
 *
 * Every method runs on the encoder's own thread, so calls from the core
 * (one at a time, from session goroutines) and the display listener never
 * race.
 */
object ScreenCapture : ScreenSource {
    private val thread = HandlerThread("remohop-screen").apply { start() }
    private val handler = Handler(thread.looper)

    private lateinit var app: Context
    @Volatile private var projection: MediaProjection? = null
    private var display: VirtualDisplay? = null
    private var codec: MediaCodec? = null
    private var listening = false

    // What the core last asked for, to restart on rotation.
    private var maxSize = 0
    private var fps = 30
    private var bitrate = 4_000_000L
    private var running = false
    private var displayW = 0
    private var displayH = 0

    @Volatile private var picW = 0
    @Volatile private var picH = 0

    /** The picture being encoded (0, 0 when none). */
    fun pictureSize(): Pair<Int, Int> = picW to picH

    /** Whether the owner has allowed screen capture for the current sharing. */
    val active: Boolean get() = projection != null

    /** Takes the projection the owner just allowed ([HostService] gets it). */
    fun attach(context: Context, p: MediaProjection, onStop: () -> Unit) = onEncoder {
        app = context.applicationContext
        projection?.let { if (it !== p) releaseLocked(stopProjection = true) }
        projection = p
        // Android 14 wants a callback before the virtual display is made.
        p.registerCallback(object : MediaProjection.Callback() {
            override fun onStop() {
                handler.post {
                    if (projection === p) {
                        Diag.log("screen: the system ended screen sharing")
                        releaseLocked(stopProjection = false)
                        onStop()
                    }
                }
            }
        }, handler)
        Diag.log("screen: screen sharing allowed")
        if (running) restart()
    }

    /** Ends the sharing: the encoder, the virtual display and the projection. */
    fun detach() = onEncoder { releaseLocked(stopProjection = true) }

    private fun onThread() = android.os.Looper.myLooper() == thread.looper

    private fun onEncoder(f: () -> Unit) {
        if (onThread()) f() else handler.post(f)
    }

    /** Runs [f] on the encoder thread and waits for it (for the core's calls). */
    private fun <T> sync(f: () -> T): T {
        if (onThread()) return f()
        val box = arrayOfNulls<Any>(1)
        var err: Throwable? = null
        val latch = java.util.concurrent.CountDownLatch(1)
        handler.post {
            try { box[0] = f() } catch (t: Throwable) { err = t } finally { latch.countDown() }
        }
        if (!latch.await(5, java.util.concurrent.TimeUnit.SECONDS)) throw IllegalStateException("the screen encoder does not answer")
        err?.let { throw it }
        @Suppress("UNCHECKED_CAST")
        return box[0] as T
    }

    // ---- ScreenSource (core threads) ----

    override fun startScreen(maxSize: Long, fps: Long, bitrate: Long) = sync {
        this.maxSize = maxSize.toInt()
        this.fps = fps.toInt().coerceIn(5, 60)
        this.bitrate = bitrate
        running = true
        if (projection == null) throw IllegalStateException("the phone's owner has not started screen sharing yet")
        restart()
    }

    override fun stopScreen() = sync {
        running = false
        stopEncoder()
        display?.surface = null
        Unit
    }

    override fun requestKeyframe() = sync {
        codec?.setParameters(Bundle().apply { putInt(MediaCodec.PARAMETER_KEY_REQUEST_SYNC_FRAME, 0) })
        Unit
    }

    override fun setBitrate(bitrate: Long) = sync {
        this.bitrate = bitrate
        codec?.setParameters(Bundle().apply { putInt(MediaCodec.PARAMETER_KEY_VIDEO_BITRATE, clampBitrate(bitrate)) })
        Unit
    }

    private fun clampBitrate(b: Long) = b.coerceIn(300_000L, 40_000_000L).toInt()

    // ---- encoder thread ----

    private val displayListener = object : DisplayManager.DisplayListener {
        override fun onDisplayAdded(displayId: Int) {}
        override fun onDisplayRemoved(displayId: Int) {}
        override fun onDisplayChanged(displayId: Int) {
            if (displayId != Display.DEFAULT_DISPLAY || !running || projection == null) return
            val (w, h) = displaySize(app)
            if (w != displayW || h != displayH) {
                Diag.log("screen: the display turned or changed size; restarting the encoder")
                restart()
            }
        }
    }

    /** (Re)starts the encoder for the current display and limits. */
    private fun restart() {
        val p = projection ?: return
        val (dw, dh) = displaySize(app)
        displayW = dw
        displayH = dh
        Remohopcore.setScreenInfo(dw.toLong(), dh.toLong(), rotation(app).toLong())
        var (w, h) = CoordMap.pictureSize(dw, dh, maxSize)
        val old = codec
        codec = null
        val enc = try {
            createEncoder(w, h)
        } catch (e: Exception) {
            // Fall back to a smaller picture, which every encoder takes.
            val small = CoordMap.pictureSize(dw, dh, 1280)
            w = small.first; h = small.second
            Diag.log("screen: the encoder refused the picture (${e.javaClass.simpleName}); trying ${w}x$h")
            createEncoder(w, h)
        }
        val surface = enc.createInputSurface()
        enc.start()
        val dpi = app.resources.displayMetrics.densityDpi
        val vd = display
        if (vd == null) {
            display = p.createVirtualDisplay("RemoHop", w, h, dpi, DisplayManager.VIRTUAL_DISPLAY_FLAG_AUTO_MIRROR, surface, null, handler)
        } else {
            vd.resize(w, h, dpi)
            vd.surface = surface
        }
        codec = enc
        surfaces[enc] = surface
        picW = w
        picH = h
        old?.let { release(it) }
        if (!listening) {
            app.getSystemService(DisplayManager::class.java)?.registerDisplayListener(displayListener, handler)
            listening = true
        }
        Diag.log("screen: encoding ${w}x$h at up to $fps fps, ${bitrate / 1000} kbit/s")
    }

    private val surfaces = HashMap<MediaCodec, Surface>()

    private fun createEncoder(w: Int, h: Int): MediaCodec {
        val enc = MediaCodec.createEncoderByType(MediaFormat.MIMETYPE_VIDEO_AVC)
        val caps = enc.codecInfo.getCapabilitiesForType(MediaFormat.MIMETYPE_VIDEO_AVC)
        enc.setCallback(callback, handler)
        try {
            try {
                enc.configure(format(w, h, caps, rich = true), null, null, MediaCodec.CONFIGURE_FLAG_ENCODE)
            } catch (e: Exception) {
                enc.reset()
                enc.setCallback(callback, handler)
                enc.configure(format(w, h, caps, rich = false), null, null, MediaCodec.CONFIGURE_FLAG_ENCODE)
            }
        } catch (e: Exception) {
            enc.release()
            throw e
        }
        return enc
    }

    private fun format(w: Int, h: Int, caps: MediaCodecInfo.CodecCapabilities, rich: Boolean): MediaFormat {
        val f = MediaFormat.createVideoFormat(MediaFormat.MIMETYPE_VIDEO_AVC, w, h)
        f.setInteger(MediaFormat.KEY_COLOR_FORMAT, MediaCodecInfo.CodecCapabilities.COLOR_FormatSurface)
        f.setInteger(MediaFormat.KEY_BIT_RATE, clampBitrate(bitrate))
        f.setInteger(MediaFormat.KEY_FRAME_RATE, fps)
        // The encoder's own key frames are a fallback: viewers ask for one when they need it.
        f.setInteger(MediaFormat.KEY_I_FRAME_INTERVAL, 10)
        // A still screen produces no frames from the mirror; repeating the
        // last one keeps acknowledgements (and key frame requests) flowing.
        f.setLong(MediaFormat.KEY_REPEAT_PREVIOUS_FRAME_AFTER, 100_000L)
        if (!rich) return f
        f.setInteger(MediaFormat.KEY_PRIORITY, 0) // real time
        if (caps.encoderCapabilities?.isBitrateModeSupported(MediaCodecInfo.EncoderCapabilities.BITRATE_MODE_VBR) == true) {
            f.setInteger(MediaFormat.KEY_BITRATE_MODE, MediaCodecInfo.EncoderCapabilities.BITRATE_MODE_VBR)
        }
        if (Build.VERSION.SDK_INT >= 29) {
            f.setFloat(MediaFormat.KEY_MAX_FPS_TO_ENCODER, fps.toFloat())
            // High profile without B-frames (the protocol has no reordering).
            if (caps.profileLevels.any { it.profile == MediaCodecInfo.CodecProfileLevel.AVCProfileHigh }) {
                f.setInteger(MediaFormat.KEY_PROFILE, MediaCodecInfo.CodecProfileLevel.AVCProfileHigh)
                f.setInteger(MediaFormat.KEY_MAX_B_FRAMES, 0)
            }
        }
        return f
    }

    private val callback = object : MediaCodec.Callback() {
        override fun onInputBufferAvailable(codec: MediaCodec, index: Int) {}

        override fun onOutputBufferAvailable(c: MediaCodec, index: Int, info: MediaCodec.BufferInfo) {
            if (c !== codec) return
            val bytes = try {
                val buf = c.getOutputBuffer(index)
                if (buf == null || info.size <= 0) null else ByteArray(info.size).also {
                    buf.position(info.offset)
                    buf.get(it, 0, info.size)
                }
            } catch (e: IllegalStateException) {
                return
            } finally {
                runCatching { c.releaseOutputBuffer(index, false) }
            }
            if (bytes == null) return
            val config = info.flags and MediaCodec.BUFFER_FLAG_CODEC_CONFIG != 0
            // The mirror stamps frames on the monotonic clock; the core wants wall-clock capture times.
            val capture = if (config) 0L else System.currentTimeMillis() * 1000 - (System.nanoTime() / 1000 - info.presentationTimeUs)
            runCatching { Remohopcore.pushScreenFrame(bytes, capture) }
        }

        override fun onError(c: MediaCodec, e: MediaCodec.CodecException) {
            if (c !== codec) return
            Diag.log("screen: encoder error ${e.diagnosticInfo.take(100)}; restarting")
            if (running && projection != null) handler.postDelayed({ if (running) restart() }, 500)
        }

        override fun onOutputFormatChanged(codec: MediaCodec, format: MediaFormat) {}
    }

    private fun stopEncoder() {
        codec?.let { release(it) }
        codec = null
        picW = 0
        picH = 0
    }

    private fun release(c: MediaCodec) {
        runCatching { c.stop() }
        runCatching { c.release() }
        surfaces.remove(c)?.release()
    }

    private fun releaseLocked(stopProjection: Boolean) {
        stopEncoder()
        display?.release()
        display = null
        if (listening) {
            runCatching { app.getSystemService(DisplayManager::class.java)?.unregisterDisplayListener(displayListener) }
            listening = false
        }
        val p = projection
        projection = null
        if (stopProjection) runCatching { p?.stop() }
    }

    // ---- the display ----

    /** The default display's size in pixels in its current rotation. */
    fun displaySize(context: Context): Pair<Int, Int> {
        val m = DisplayMetrics()
        @Suppress("DEPRECATION")
        context.getSystemService(DisplayManager::class.java)?.getDisplay(Display.DEFAULT_DISPLAY)?.getRealMetrics(m)
        return m.widthPixels to m.heightPixels
    }

    private fun rotation(context: Context): Int {
        val r = context.getSystemService(DisplayManager::class.java)?.getDisplay(Display.DEFAULT_DISPLAY)?.rotation ?: Surface.ROTATION_0
        return when (r) {
            Surface.ROTATION_90 -> 90
            Surface.ROTATION_180 -> 180
            Surface.ROTATION_270 -> 270
            else -> 0
        }
    }
}
