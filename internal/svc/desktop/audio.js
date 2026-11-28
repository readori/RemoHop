// audio.js: the host's audio played in the page. Opus packets from the
// host ('A' messages), decoded with WebCodecs and played with the Web Audio
// API. Shared by the standalone viewer (viewer.html, which gets it inlined
// where it is served) and the web console's Desktop tab (internal/web
// serves it as assets/audio.js). Each page asks its gateway for the stream
// and keeps its own buttons and wording.
//
// Each packet carries the time its sound was captured on the host (already
// translated to the gateway's clock, which the page treats as its own).
// The sound is scheduled to leave the speakers at capture time + a delay,
// so playback is steady whatever the arrival jitter: the delay is the
// larger of a jitter buffer (40 ms plus three times the measured arrival
// jitter, at most 300 ms) and the video's own capture-to-display latency,
// so lips and sound line up. Packets that come too late to be scheduled are
// played at once and the schedule slips; when more than 120 ms of sound is
// queued beyond the target, the excess is dropped.
'use strict';
window.RemoHopAudio = (function () {
  const cfg = {codec: 'opus', sampleRate: 48000, numberOfChannels: 2};
  // Binary message: 'A', sequence (4), capture time (8, microseconds),
  // arrival time (8), then one Opus packet.
  const headerLen = 21;

  // unsupported resolves to why this browser cannot play the host's
  // audio: 'nodecoder' (no WebCodecs AudioDecoder or Web Audio), 'noopus',
  // or '' when it can.
  async function unsupported() {
    if (typeof AudioDecoder === 'undefined' || typeof AudioContext === 'undefined') return 'nodecoder';
    try { if ((await AudioDecoder.isConfigSupported(cfg)).supported) return ''; } catch (e) { /* unsupported */ }
    return 'noopus';
  }

  // isAudio tells an audio message from video.
  const isAudio = buf => buf.byteLength > 0 && new Uint8Array(buf, 0, 1)[0] === 0x41;

  // player plays the stream. nowUs is the page's wall clock in
  // microseconds; videoMs the video's capture-to-display latency (0 if
  // unknown).
  function player(nowUs, videoMs) {
    const au = {ctx: null, gain: null, dec: null, on: false, muted: false, cursor: 0, jitter: 0, prevTransit: null, delayUs: 0,
                s: {packets: 0, decoded: 0, bytes: 0, underruns: 0, trimmed: 0, errors: 0, rms: 0, peak: 0,
                    bufMs: 0, delayMs: 0, skewMs: 0, jitterMs: 0}};

    // prepare makes the audio context and resumes it: browsers start
    // audio only after a user gesture, so call it from one.
    function prepare() {
      if (!au.ctx) {
        au.ctx = new AudioContext({latencyHint: 'interactive', sampleRate: 48000});
        au.gain = au.ctx.createGain();
        au.gain.gain.value = au.muted ? 0 : 1;
        au.gain.connect(au.ctx.destination);
      }
      return resume();
    }
    function resume() {
      if (au.ctx && au.ctx.state === 'suspended') return au.ctx.resume().catch(() => {});
      return Promise.resolve();
    }

    // begin starts playing a new stream; reset stops.
    function begin() {
      if (au.dec && au.dec.state !== 'closed') au.dec.close();
      au.dec = new AudioDecoder({output: onData, error: e => { au.s.errors++; console.warn('audio decoder', e); }});
      au.dec.configure(cfg);
      au.on = true;
      au.cursor = 0;
      au.prevTransit = null;
    }
    function reset() {
      if (au.dec && au.dec.state !== 'closed') au.dec.close();
      au.dec = null;
      au.on = false;
      au.cursor = 0;
      au.prevTransit = null;
    }

    function packet(buf) {
      if (!au.dec || au.dec.state !== 'configured' || buf.byteLength < headerLen + 1) return;
      const dv = new DataView(buf);
      const cap = Number(dv.getBigInt64(5));
      const arr = Number(dv.getBigInt64(13));
      const data = new Uint8Array(buf, headerLen);
      au.s.packets++;
      au.s.bytes += data.length;
      // Arrival jitter as in RFC 3550: how much the transit time varies.
      const transit = arr - cap;
      if (au.prevTransit !== null) au.jitter += (Math.abs(transit - au.prevTransit) - au.jitter) / 16;
      au.prevTransit = transit;
      au.s.jitterMs = au.jitter / 1000;
      if (au.dec.decodeQueueSize > 10) { au.s.trimmed++; return; }
      try {
        au.dec.decode(new EncodedAudioChunk({type: 'key', timestamp: cap, data}));
      } catch (e) { au.s.errors++; }
    }

    function onData(ad) {
      try {
        if (!au.ctx) return;
        const n = ad.numberOfFrames, chs = ad.numberOfChannels;
        const buf = au.ctx.createBuffer(chs, n, ad.sampleRate);
        const tmp = new Float32Array(n);
        let sq = 0;
        for (let c = 0; c < chs; c++) {
          ad.copyTo(tmp, {planeIndex: c, format: 'f32-planar'});
          buf.copyToChannel(tmp, c);
          if (c === 0) for (let i = 0; i < n; i++) { sq += tmp[i] * tmp[i]; au.s.peak = Math.max(au.s.peak * 0.9995, Math.abs(tmp[i])); }
        }
        const rms = Math.sqrt(sq / n);
        au.s.rms = au.s.rms === 0 ? rms : au.s.rms + (rms - au.s.rms) / 8;
        au.s.decoded++;
        schedule(buf, ad.timestamp);
      } finally {
        ad.close();
      }
    }

    function schedule(buf, capUs) {
      const ctx = au.ctx;
      const v = videoMs();
      const videoUs = v > 0 ? v * 1000 : 0;
      const target = Math.max(Math.min(300000, Math.max(40000, 40000 + 3 * au.jitter)), videoUs + 5000);
      au.delayUs = au.delayUs === 0 ? target : au.delayUs + (target - au.delayUs) / 32;
      const outLat = (ctx.outputLatency || 0) + (ctx.baseLatency || 0);
      const t = ctx.currentTime;
      const when = t + (capUs + au.delayUs - nowUs()) / 1e6 - outLat;
      let start;
      if (au.cursor === 0 || au.cursor < t) {
        if (au.cursor !== 0) au.s.underruns++;
        start = Math.max(when, t + 0.005);
      } else if (Math.abs(when - au.cursor) <= 0.02) {
        start = au.cursor;                       // gapless
      } else if (when > au.cursor) {
        start = when;                            // a pause in the sound, or the delay grew
      } else if (au.cursor - when > 0.12) {
        au.s.trimmed++;                          // too much queued: drop to cut the delay
        return;
      } else {
        start = au.cursor;
      }
      const src = ctx.createBufferSource();
      src.buffer = buf;
      src.connect(au.gain);
      src.start(start);
      au.cursor = start + buf.duration;
      au.s.delayMs = au.delayUs / 1000;
      au.s.skewMs = (au.delayUs - videoUs) / 1000;
    }

    function mute(on) {
      au.muted = on;
      if (au.gain) au.gain.gain.setTargetAtTime(on ? 0 : 1, au.ctx.currentTime, 0.01);
    }

    return {
      prepare, resume, begin, reset, packet, mute,
      get on() { return au.on; },
      get muted() { return au.muted; },
      // stalled: playing, but the browser holds the sound until a gesture.
      stalled: () => au.on && !!au.ctx && au.ctx.state === 'suspended',
      ctxState: () => au.ctx ? au.ctx.state : 'none',
      stats() {
        if (au.ctx) au.s.bufMs = Math.max(0, (au.cursor - au.ctx.currentTime) * 1000);
        return au.s;
      },
    };
  }

  return {unsupported, isAudio, player};
})();
