package com.wacli.expo

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import androidx.core.app.NotificationCompat

/**
 * Keeps the process alive so wacli's WhatsApp socket survives backgrounding.
 *
 * This service holds no state and starts nothing: the Go runtime is already running, started by
 * WacliModule, and its lifetime is the process's. All this does is tell Android the process is
 * doing user-visible work, which is the only way to avoid being frozen or killed once the app
 * leaves the foreground.
 *
 * The persistent notification is not optional — Android requires one for a foreground service, and
 * an app that hides it does not get to keep running.
 *
 * Two caveats worth knowing before relying on this. Doze and the aggressive OEM task-killers
 * (Xiaomi, Oppo, Samsung and friends) will still kill the process on their own schedule; wacli's
 * connection watchdog reconnects when that happens, but messages that arrive while it is dead are
 * fetched on the next sync rather than delivered live. And on Android 14+ the dataSync service type
 * carries a runtime budget, after which the system stops the service; see the README.
 */
class WacliForegroundService : Service() {

  override fun onCreate() {
    super.onCreate()
    createNotificationChannel()
  }

  override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
    val notification = buildNotification()
    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
      startForeground(NOTIFICATION_ID, notification, ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC)
    } else {
      startForeground(NOTIFICATION_ID, notification)
    }
    // START_STICKY so the system brings the service back after killing it for memory. The Go side
    // reconnects on its own once the process is alive again.
    return START_STICKY
  }

  override fun onBind(intent: Intent?): IBinder? = null

  private fun createNotificationChannel() {
    if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) {
      return
    }
    val channel = NotificationChannel(
      CHANNEL_ID,
      "WhatsApp connection",
      // LOW keeps it silent and unobtrusive: it is a status indicator, not something to interrupt
      // the user with. It cannot be hidden entirely.
      NotificationManager.IMPORTANCE_LOW,
    ).apply {
      description = "Keeps the WhatsApp connection alive while the app is in the background."
      setShowBadge(false)
    }
    getSystemService(NotificationManager::class.java).createNotificationChannel(channel)
  }

  private fun buildNotification(): Notification {
    // Tapping the notification should reopen the app rather than do nothing, so reuse whatever the
    // launcher intent is — the module has no way to know the host app's main activity by name.
    val launchIntent = packageManager.getLaunchIntentForPackage(packageName)?.apply {
      flags = Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP
    }
    val pendingIntent = launchIntent?.let {
      PendingIntent.getActivity(
        this,
        0,
        it,
        PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
      )
    }
    return NotificationCompat.Builder(this, CHANNEL_ID)
      .setContentTitle("WhatsApp connected")
      .setContentText("wacli is keeping your WhatsApp session active.")
      .setSmallIcon(applicationInfo.icon)
      .setOngoing(true)
      .setPriority(NotificationCompat.PRIORITY_LOW)
      .setContentIntent(pendingIntent)
      .build()
  }

  private companion object {
    const val CHANNEL_ID = "wacli_connection"
    const val NOTIFICATION_ID = 1_9890
  }
}
