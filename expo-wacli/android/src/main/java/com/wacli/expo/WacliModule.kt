package com.wacli.expo

import android.content.Context
import android.content.Intent
import android.os.Build
import android.util.Base64
import androidx.core.os.bundleOf
import com.wacli.mobile.EventHandler
import com.wacli.mobile.LoginHandler
import com.wacli.mobile.Mobile
import com.wacli.mobile.VoiceHandler
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

  /// The handler bridging the call in progress back to JavaScript. Held so it cannot be collected
  /// while Go still holds the other end. Not cleared in onEnded — it is replaced on the next call
  /// and dropped if a call fails to start.
  private var voiceBridge: VoiceHandler? = null

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
      "onVoiceState",
      "onVoiceTranscript",
      "onVoiceEnded",
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

    AsyncFunction("exec") { line: String -> Mobile.exec(line) }

    AsyncFunction("execCommands") { Mobile.execCommands() }

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

    // ---- streaming voice ----
    //
    // Audio does not appear here. Go owns the relay socket end to end; this boundary carries only
    // the cached lines going in and state and transcripts coming back.

    AsyncFunction("addCachedLine") { id: String, pcm: String ->
      Mobile.addCachedLine(id, Base64.decode(pcm, Base64.DEFAULT))
    }

    // Synchronous, matching the TypeScript declaration: an AsyncFunction here would hand JavaScript
    // a promise where it expects void, and the mismatch is invisible until a caller awaits nothing.
    Function("clearCachedLines") { Mobile.clearCachedLines() }

    AsyncFunction("startVoiceCall") {
      to: String, relayUrl: String, token: String, language: String, voice: String ->
      val bridge = voiceHandler()
      // Held for the duration of the call: this returns as soon as the call is *offered*, with the
      // whole conversation still ahead of it, and Go calls back into this object throughout.
      voiceBridge = bridge
      try {
        Mobile.startVoiceCall(to, relayUrl, token, language, voice, bridge)
      } catch (e: Exception) {
        // No call was placed, so onEnded will never fire and nothing else will clear this.
        voiceBridge = null
        throw e
      }
    }

    AsyncFunction("endVoiceCall") { reason: String -> Mobile.endVoiceCall(reason) }

    // ---- session handover ----

    AsyncFunction("exportSession") {
      // Go stops the daemon before it reads, so the notification has to come down with it —
      // otherwise it goes on advertising a WhatsApp connection that was closed seconds ago.
      stopForegroundService()
      // NO_WRAP: the platform default breaks the output every 76 characters, and this string goes
      // straight into a JSON body.
      Base64.encodeToString(Mobile.exportSession(), Base64.NO_WRAP)
    }

    AsyncFunction("importSession") { base64: String ->
      // Deliberately does not stop the service first. Go refuses the import outright while the
      // daemon is live, and that refusal is the guardrail — swapping the store under an open
      // connection is the one mistake here with no cheap recovery.
      Mobile.importSession(Base64.decode(base64, Base64.DEFAULT))
    }

    AsyncFunction("hasSession") { Mobile.hasSession() }

    AsyncFunction("releaseSession") {
      stopForegroundService()
      Mobile.releaseSession()
    }
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

  /**
   * Adapts the Go VoiceHandler interface to events.
   *
   * Callbacks arrive on Go goroutines rather than the main thread, which matches how the event
   * handler above already delivers — sendEvent is what crosses back to JavaScript, and nothing here
   * touches a View.
   */
  private fun voiceHandler() = object : VoiceHandler {
    override fun onState(state: String) =
      sendEvent("onVoiceState", bundleOf("state" to state))

    override fun onTranscript(text: String, final: Boolean) =
      sendEvent("onVoiceTranscript", bundleOf("text" to text, "final" to final))

    override fun onEnded(reason: String) =
      sendEvent("onVoiceEnded", bundleOf("reason" to reason))
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
