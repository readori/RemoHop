plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

// The Go core, produced by ../build.sh (gomobile bind). Not committed.
val coreAar = file("libs/remohopcore.aar")

android {
    namespace = "com.remohop.app"
    compileSdk = 35

    defaultConfig {
        applicationId = "com.remohop.app"
        minSdk = 26
        targetSdk = 35
        versionCode = (findProperty("remohop.versionCode") as String?)?.toInt() ?: 1
        versionName = (findProperty("remohop.versionName") as String?) ?: "0.1.0"
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
        ndk { abiFilters += listOf("arm64-v8a", "x86_64") }
    }

    buildTypes {
        release {
            isMinifyEnabled = true
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
            // Signing is configured by the release pipeline, not here.
        }
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    buildFeatures { buildConfig = true }
    packaging {
        // gomobile's libgojni.so must stay uncompressed and page aligned.
        jniLibs { useLegacyPackaging = false }
    }
    sourceSets["main"].assets.srcDir(layout.buildDirectory.dir("generated/remohopAssets"))
    testOptions { unitTests.isReturnDefaultValues = false }
    lint { abortOnError = true; warningsAsErrors = false }
}

kotlin {
    compilerOptions { jvmTarget.set(org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17) }
}

// The script the WebView injects is shared with the iOS app.
val copySharedScript by tasks.registering(Copy::class) {
    from(rootProject.file("../shared/remohop-mobile.js"))
    into(layout.buildDirectory.dir("generated/remohopAssets"))
}
tasks.named("preBuild") {
    dependsOn(copySharedScript)
    doFirst {
        check(coreAar.exists()) { "app/libs/remohopcore.aar is missing: run mobile/android/build.sh (gomobile bind) first" }
    }
}

dependencies {
    implementation(files(coreAar))
    implementation("androidx.core:core-ktx:1.16.0")
    implementation("androidx.webkit:webkit:1.14.0")

    testImplementation("junit:junit:4.13.2")
    testImplementation("org.json:json:20250517")

    androidTestImplementation("androidx.test:runner:1.7.0")
    androidTestImplementation("androidx.test:core:1.7.0")
    androidTestImplementation("androidx.test.ext:junit:1.3.0")
}
