package com.remohop.app

import android.app.Application

class RemoHopApp : Application() {
    override fun onCreate() {
        super.onCreate()
        Core.init(this)
        LocalAddresses.watch(this)
    }
}
