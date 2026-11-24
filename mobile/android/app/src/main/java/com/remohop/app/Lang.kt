package com.remohop.app

import android.app.LocaleManager
import android.content.Context
import android.content.res.Configuration
import android.content.res.Resources
import android.os.Build
import android.os.LocaleList
import java.util.Locale

/**
 * The app's language: follow the system (Chinese for any zh-* locale,
 * otherwise English), Simplified Chinese, or English, picked in Settings.
 *
 * Android 13 and later keep the choice as the app's own locale
 * (LocaleManager), so it is the same choice the system's per-app language
 * setting shows (res/xml/locales_config.xml lists the two languages).
 * Older versions keep it in the app's preferences. Either way the native
 * screens get their resources through [wrap] or [localized], and the web
 * page gets "zh" or "en" from [effective] (?lang= in its address, then
 * remohopMobile.setLang when it changes).
 */
object Lang {
    const val SYSTEM = "system"
    const val ZH = "zh"
    const val EN = "en"
    private const val PREFS = "remohop.ui"
    private const val KEY = "language"

    /** [SYSTEM], [ZH] or [EN]. */
    fun choice(context: Context): String {
        if (Build.VERSION.SDK_INT >= 33) {
            val locales = context.getSystemService(LocaleManager::class.java)?.applicationLocales
            if (locales != null) return if (locales.isEmpty) SYSTEM else of(locales[0].language)
        }
        return when (val v = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE).getString(KEY, SYSTEM)) {
            ZH, EN -> v
            else -> SYSTEM
        }
    }

    /**
     * Stores [choice]. On Android 13 and later the system then recreates
     * the activities that do not handle locale changes themselves; on older
     * versions the caller recreates its own.
     */
    fun setChoice(context: Context, choice: String) {
        val c = if (choice == ZH || choice == EN) choice else SYSTEM
        context.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit().putString(KEY, c).apply()
        if (Build.VERSION.SDK_INT >= 33) {
            context.getSystemService(LocaleManager::class.java)?.applicationLocales =
                if (c == SYSTEM) LocaleList.getEmptyLocaleList() else LocaleList.forLanguageTags(if (c == ZH) "zh-Hans" else "en")
        }
    }

    /** "zh" or "en": the language the app shows now. */
    fun effective(context: Context): String = when (val c = choice(context)) {
        SYSTEM -> of(Resources.getSystem().configuration.locales[0]?.language)
        else -> c
    }

    private fun of(language: String?) = if (language == "zh") ZH else EN

    private fun locale(lang: String): Locale = if (lang == ZH) Locale.SIMPLIFIED_CHINESE else Locale.ENGLISH

    private fun configured(base: Context, lang: String): Context {
        val config = Configuration(base.resources.configuration)
        config.setLocale(locale(lang))
        return base.createConfigurationContext(config)
    }

    /**
     * [base] with its resources in the effective language, for an
     * activity's attachBaseContext. Any zh-* locale (zh-TW too) gets the
     * Simplified Chinese strings.
     */
    fun wrap(base: Context): Context = configured(base, effective(base))

    @Volatile private var cached: Pair<String, Context>? = null

    /**
     * A context whose resources follow the current choice, for an activity
     * that outlives a change of it (MainActivity keeps its web page and
     * sessions instead of being recreated).
     */
    fun localized(context: Context): Context {
        val lang = effective(context)
        cached?.let { if (it.first == lang) return it.second }
        return configured(context.applicationContext, lang).also { cached = lang to it }
    }
}
