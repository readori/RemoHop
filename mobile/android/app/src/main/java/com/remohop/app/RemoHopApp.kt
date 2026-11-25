package com.remohop.app

import android.app.Application

class RemoHopApp : Application() {
    override fun onCreate() {
        super.onCreate()
        CrashLog.install(this)
        Core.init(this)
        CrashLog.flush()
        LocalAddresses.watch(this)
    }
}
