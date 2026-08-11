package com.wacli.expo

import android.content.Context
import android.content.Intent
import android.os.Build
import androidx.core.os.bundleOf
import com.wacli.mobile.EventHandler
import com.wacli.mobile.LoginHandler
import com.wacli.mobile.Mobile
import expo.modules.kotlin.exception.CodedException
import expo.modules.kotlin.modules.Module
import expo.modules.kotlin.modules.ModuleDefinition
import java.util.concurrent.atomic.AtomicBoolean

/**
 * Expo module bridging JavaScript to the wacli Go runtime.
 *
 * The Go side is bound with gomobile into `com.wacli.mobile.Mobile` — see scripts/build-bindings.sh
 * — and exposes one process-wide instance, so this module is a thin, stateless adapter over static
 * calls. Everything blocking is declared with AsyncFunction, which Expo runs off the JS thread.
 */
class WacliModule : Module() {

  private val configured = AtomicBoolean(false)

  private val context: Context
    get() = appContext.reactContext ?: throw MissingContextException()

  override fun definition() = ModuleDefinition {
    Name("Wacli")

    Events(
      "onEvent",
      "onLoginQRCode",
      "onLoginPairingCode",
      "onLoginStatus",
      "onLoginError",
    )

    OnCreate {
      // Registered once for the lifetime of the module rather than per-start, so an event arriving
      // during a reconnect is not dropped between a stop and the next start.
      Mobile.setEventHandler(EventHandler { event, payloadJson ->
        sendEvent("onEvent", bundleOf("event" to event, "payload" to payloadJson))
      })
    }

    OnDestroy {
      // The JS context is going away; nothing is left to receive events. The Go service keeps
      // running if the foreground service is up, which is the point of it.
      Mobile.setEventHandler(null)
    }

    AsyncFunction("start") {
      ensureConfigured()
      Mobile.start()
      startForegroundService()
    }

    AsyncFunction("stop") {
      stopForegroundService()
      Mobile.stop()
    }

    AsyncFunction("isPaired") {
      ensureConfigured()
      Mobile.isPaired()
    }

    AsyncFunction("isRunning") { Mobile.isRunning() }

    AsyncFunction("isConnected") { Mobile.isConnected() }

    AsyncFunction("request") { method: String, path: String, body: String? ->
      Mobile.request(method, path, body ?: "")
    }

    AsyncFunction("loginWithQR") {
      ensureConfigured()
      Mobile.startLogin(loginHandler())
      startForegroundService()
    }

    AsyncFunction("loginWithPhone") { phone: String ->
      ensureConfigured()
      Mobile.startPairingLogin(loginHandler(), phone)
      startForegroundService()
    }

    AsyncFunction("cancelLogin") { Mobile.cancelLogin() }

    AsyncFunction("logout") {
      stopForegroundService()
      Mobile.logout()
    }

    AsyncFunction("getVersion") { Mobile.version() }
  }

  /**
   * Point Go at the app's private data directory.
   *
   * There is no home directory on Android for wacli's usual ~/.wacli to resolve against, so this
   * has to happen before any other call. filesDir is inside the app sandbox, which is what keeps
   * the session keys and the message history unreadable by other apps — do not move this to
   * external storage.
   */
  private fun ensureConfigured() {
    if (configured.compareAndSet(false, true)) {
      try {
        Mobile.configure(context.filesDir.absolutePath)
      } catch (e: Exception) {
        configured.set(false)
        throw e
      }
    }
  }

  private fun loginHandler() = object : LoginHandler {
    override fun onQRCode(code: String) =
      sendEvent("onLoginQRCode", bundleOf("code" to code))

    override fun onPairingCode(code: String) =
      sendEvent("onLoginPairingCode", bundleOf("code" to code))

    override fun onStatus(status: String) =
      sendEvent("onLoginStatus", bundleOf("status" to status))

    override fun onError(message: String) =
      sendEvent("onLoginError", bundleOf("message" to message))
  }

  private fun startForegroundService() {
    val intent = Intent(context, WacliForegroundService::class.java)
    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
      context.startForegroundService(intent)
    } else {
      context.startService(intent)
    }
  }

  private fun stopForegroundService() {
    context.stopService(Intent(context, WacliForegroundService::class.java))
  }
}

internal class MissingContextException :
  CodedException("React context is unavailable; the app is probably tearing down")
