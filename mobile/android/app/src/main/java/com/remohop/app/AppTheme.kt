package com.remohop.app

import android.content.Context
import android.content.res.Configuration
import android.view.ContextThemeWrapper
import com.google.android.material.color.DynamicColors
import com.google.android.material.color.MaterialColors
import org.json.JSONObject
import com.google.android.material.R as M

/**
 * The app's Material 3 colour roles outside the Settings screens: the
 * Settings theme with dynamic colour (Material You, Android 12+) where the
 * device has it, the cobalt scheme otherwise. The web console takes them
 * (bridge "theme", see remohop-mobile.js) so it matches Settings and the
 * wallpaper; the main window paints the system bars in [pageColor].
 */
object AppTheme {
    /** Role name in the page's CSS (--md-NAME) to theme attribute. */
    private val ROLES = linkedMapOf(
        "primary" to M.attr.colorPrimary,
        "on-primary" to M.attr.colorOnPrimary,
        "primary-container" to M.attr.colorPrimaryContainer,
        "on-primary-container" to M.attr.colorOnPrimaryContainer,
        "secondary-container" to M.attr.colorSecondaryContainer,
        "on-secondary-container" to M.attr.colorOnSecondaryContainer,
        "tertiary-container" to M.attr.colorTertiaryContainer,
        "on-tertiary-container" to M.attr.colorOnTertiaryContainer,
        "surface" to M.attr.colorSurface,
        "surface-bright" to M.attr.colorSurfaceBright,
        "surface-container-lowest" to M.attr.colorSurfaceContainerLowest,
        "surface-container-low" to M.attr.colorSurfaceContainerLow,
        "surface-container" to M.attr.colorSurfaceContainer,
        "surface-container-high" to M.attr.colorSurfaceContainerHigh,
        "surface-container-highest" to M.attr.colorSurfaceContainerHighest,
        "on-surface" to M.attr.colorOnSurface,
        "on-surface-variant" to M.attr.colorOnSurfaceVariant,
        "outline" to M.attr.colorOutline,
        "outline-variant" to M.attr.colorOutlineVariant,
        "error" to M.attr.colorError,
        "error-container" to M.attr.colorErrorContainer,
        "on-error-container" to M.attr.colorOnErrorContainer,
        "inverse-surface" to M.attr.colorSurfaceInverse,
        "inverse-on-surface" to M.attr.colorOnSurfaceInverse,
    )

    /** The Settings theme (with dynamic colour where available) in the light or dark scheme. */
    private fun themed(base: Context, night: Boolean): Context {
        val cfg = Configuration(base.resources.configuration)
        cfg.uiMode = (cfg.uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
            (if (night) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO)
        return DynamicColors.wrapContextIfAvailable(ContextThemeWrapper(base.createConfigurationContext(cfg), R.style.Theme_RemoHop_Settings))
    }

    private fun isNight(c: Context) = (c.resources.configuration.uiMode and Configuration.UI_MODE_NIGHT_MASK) == Configuration.UI_MODE_NIGHT_YES

    private fun hex(c: Int) = String.format("#%06x", c and 0xffffff)

    /** {"light": {role: "#rrggbb"}, "dark": {...}, "dynamic": bool} for the page. */
    fun webRoles(base: Context): JSONObject {
        val out = JSONObject().put("dynamic", DynamicColors.isDynamicColorAvailable())
        for (night in listOf(false, true)) {
            val c = themed(base, night)
            val o = JSONObject()
            for ((name, attr) in ROLES) o.put(name, hex(MaterialColors.getColor(c, attr, "AppTheme")))
            out.put(if (night) "dark" else "light", o)
        }
        return out
    }

    /**
     * The page colour of the console and Settings: surfaceContainer in the
     * light scheme, surface in the dark one (see style.css and [SettingsUi]).
     */
    fun pageColor(base: Context): Int {
        val night = isNight(base)
        return MaterialColors.getColor(themed(base, night), if (night) M.attr.colorSurface else M.attr.colorSurfaceContainer, "AppTheme")
    }
}
