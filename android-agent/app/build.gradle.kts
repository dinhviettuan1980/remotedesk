import java.util.Properties

plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

// Release key lives outside the repo (scripts/build.sh creates it on first
// build). Every APK must be signed with the same key, or Android refuses to
// install it over the previous one.
val signingProps = Properties().apply {
    val f = File(System.getProperty("user.home"), ".android/remotedesk-release.properties")
    if (f.exists()) f.inputStream().use { load(it) }
}

android {
    namespace = "vn.tuandv.remotedesk.android"
    compileSdk = 34

    defaultConfig {
        applicationId = "vn.tuandv.remotedesk.android"
        minSdk = 26
        targetSdk = 30
        versionCode = 3
        versionName = "0.1.2"
    }

    signingConfigs {
        if (signingProps.isNotEmpty()) {
            create("release") {
                storeFile = File(signingProps.getProperty("storeFile"))
                storePassword = signingProps.getProperty("storePassword")
                keyAlias = signingProps.getProperty("keyAlias")
                keyPassword = signingProps.getProperty("keyPassword")
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            signingConfig = signingConfigs.findByName("release") ?: signingConfigs.getByName("debug")
        }
    }

    buildFeatures {
        buildConfig = true
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
    lint {
        // targetSdk 30 is deliberate: the phone is sideloaded, not on Play.
        disable += setOf("ExpiredTargetSdkVersion", "OldTargetApi")
    }
}

dependencies {
    implementation("com.squareup.okhttp3:okhttp:4.12.0")
}
