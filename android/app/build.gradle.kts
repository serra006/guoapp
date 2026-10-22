import java.util.Properties

plugins {
    id("com.android.application")
    id("dev.flutter.flutter-gradle-plugin")
}

val releaseKey = rootProject.file("key.properties")
val releaseProperties = Properties()
if (releaseKey.exists()) {
    releaseKey.inputStream().use { releaseProperties.load(it) }
}

android {
    namespace = "com.duanju.duanju_app"
    compileSdk = flutter.compileSdkVersion
    ndkVersion = "28.2.13676358"

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    defaultConfig {
        applicationId = "com.duanju.duanju_app"
        minSdk = 26
        targetSdk = flutter.targetSdkVersion
        versionCode = flutter.versionCode
        versionName = flutter.versionName
        manifestPlaceholders["appLabel"] = "真果鉴"
        manifestPlaceholders["appBanner"] = "@drawable/tv_banner_all_sources"
    }

    flavorDimensions += "platform"
    productFlavors {
        // 手机/平板：包名 com.duanju.zhenguojian
        create("phone") {
            dimension = "platform"
            applicationId = "com.duanju.zhenguojian"
        }
        // 电视/盒子：包名 com.duanju.zhenguo.tv（与历史「真果鉴 TV」的
        // com.duanju.zhenguojian.tv 不同，两者可并存，互不覆盖）
        create("tv") {
            dimension = "platform"
            applicationId = "com.duanju.zhenguo.tv"
            versionNameSuffix = "-tv"
            manifestPlaceholders["appLabel"] = "真果TV"
            manifestPlaceholders["appBanner"] = "@drawable/tv_banner_all_sources"
        }
    }

    signingConfigs {
        if (releaseKey.exists()) {
            create("release") {
                val rawStore = requireNotNull(releaseProperties.getProperty("storeFile"))
                storeFile = if (file(rawStore).exists()) file(rawStore) else rootProject.file(rawStore)
                storePassword = requireNotNull(releaseProperties.getProperty("storePassword"))
                keyAlias = requireNotNull(releaseProperties.getProperty("keyAlias"))
                keyPassword = requireNotNull(releaseProperties.getProperty("keyPassword"))
            }
        }
    }

    buildTypes {
        debug {
            applicationIdSuffix = ".debug"
        }
        release {
            signingConfig = signingConfigs.getByName(if (releaseKey.exists()) "release" else "debug")
        }
    }

    packaging {
        jniLibs {
            useLegacyPackaging = true
        }
    }
}

kotlin {
    compilerOptions {
        jvmTarget = org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17
    }
}

flutter { source = "../.." }

tasks.withType<JavaCompile>().configureEach {
    if (name.contains("Release")) {
        doFirst {
            val registrant = file("src/main/java/io/flutter/plugins/GeneratedPluginRegistrant.java")
            if (registrant.exists()) {
                val generated = registrant.readText()
                val integrationPlugin = Regex(
                    """(?s)    try \{\s*flutterEngine\.getPlugins\(\)\.add\(new dev\.flutter\.plugins\.integration_test\.IntegrationTestPlugin\(\)\);\s*\} catch \(Exception e\) \{[^}]*\}\s*"""
                )
                val release = generated.replace(integrationPlugin, "")
                if (release != generated) registrant.writeText(release)
            }
        }
    }
}
