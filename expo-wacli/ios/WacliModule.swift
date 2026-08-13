import ExpoModulesCore
import Mobile
import UIKit

/**
 Expo module bridging JavaScript to the wacli Go runtime.

 The Go side is bound with gomobile into the `Mobile` framework — see scripts/build-bindings.sh —
 and exposes one process-wide instance, so this module is a thin adapter over free functions.

 # Background execution

 iOS gives a suspended app no way to hold a socket open. There is no entitlement for it: an app
 leaving the foreground gets roughly thirty seconds and is then frozen, and none of the declarable
 background modes covers "keep a WhatsApp connection". So rather than pretend, this module shuts
 the service down cleanly when the app backgrounds and brings it back when it returns — which at
 least guarantees SQLite closes its write-ahead log properly instead of being frozen mid-write, and
 that the app never resumes onto a socket that died while it was away.

 Messages that arrive while the app is backgrounded are not lost — they are fetched from WhatsApp
 on the next sync — but they do not arrive live, and no notification fires for them. An app that
 needs live delivery on iOS has to run the daemon off-device and push to it.
 */
public class WacliModule: Module {

  /// Whether the service was running when the app last backgrounded, so foregrounding knows
  /// whether to bring it back or leave it alone.
  private var shouldResumeOnForeground = false
  private var configured = false

  /// Held only while a login is in flight and the app is in the background. See `beginLoginGrace`.
  private var loginGrace: UIBackgroundTaskIdentifier = .invalid

  /// The handler bridging the call in progress back to JavaScript. See `startVoiceCall` for why it
  /// is held rather than passed and forgotten. Not cleared in `onEnded` — releasing an object from
  /// inside its own callback is a use-after-free waiting for a slow day — so it is replaced on the
  /// next call and dropped if the call fails to start.
  private var voiceBridge: VoiceBridge?

  public func definition() -> ModuleDefinition {
    Name("Wacli")

    Events(
      "onEvent",
      "onLoginQRCode",
      "onLoginPairingCode",
      "onLoginStatus",
      "onLoginError",
      "onVoiceState",
      "onVoiceTranscript",
      "onVoiceEnded"
    )

    OnCreate {
      // Registered once for the module's lifetime rather than per-start, so an event arriving
      // during a reconnect is not dropped between a stop and the next start.
      MobileSetEventHandler(EventBridge { [weak self] event, payload in
        self?.sendEvent("onEvent", ["event": event, "payload": payload])
      })
    }

    OnDestroy {
      MobileSetEventHandler(nil)
    }

    AsyncFunction("start") { () throws in
      try self.ensureConfigured()
      try wacli { MobileStart($0) }
    }

    AsyncFunction("stop") { () throws in
      try wacli { MobileStop($0) }
    }

    AsyncFunction("isPaired") { () throws -> Bool in
      try self.ensureConfigured()
      return MobileIsPaired()
    }

    AsyncFunction("isRunning") { () -> Bool in
      MobileIsRunning()
    }

    AsyncFunction("isConnected") { () -> Bool in
      MobileIsConnected()
    }

    AsyncFunction("request") { (method: String, path: String, body: String?) throws -> String in
      try wacli { MobileRequest(method, path, body ?? "", $0) }
    }

    AsyncFunction("exec") { (line: String) throws -> String in
      try wacli { MobileExec(line, $0) }
    }

    AsyncFunction("execCommands") { () -> String in
      MobileExecCommands()
    }

    AsyncFunction("loginWithQR") { () throws in
      try self.ensureConfigured()
      try wacli { MobileStartLogin(self.loginBridge(), $0) }
    }

    AsyncFunction("loginWithPhone") { (phone: String) throws in
      try self.ensureConfigured()
      try wacli { MobileStartPairingLogin(self.loginBridge(), phone, $0) }
    }

    AsyncFunction("cancelLogin") {
      MobileCancelLogin()
    }

    AsyncFunction("logout") { () throws in
      try wacli { MobileLogout($0) }
    }

    AsyncFunction("getVersion") { () -> String in
      MobileVersion()
    }

    // ---- streaming voice ----
    //
    // Audio does not appear here. Go owns the relay socket end to end; this boundary carries only
    // the cached lines going in and state and transcripts coming back.

    AsyncFunction("addCachedLine") { (id: String, pcm: String) throws in
      guard let data = Data(base64Encoded: pcm) else {
        throw InvalidBase64Exception("pcm")
      }
      try wacli { MobileAddCachedLine(id, data, $0) }
    }

    // Synchronous, matching the TypeScript declaration: an AsyncFunction here would hand JavaScript
    // a promise where it expects void, and the mismatch is invisible until a caller awaits nothing.
    Function("clearCachedLines") {
      MobileClearCachedLines()
    }

    AsyncFunction("startVoiceCall") {
      (to: String, relayURL: String, token: String, language: String, voice: String) throws in
      let bridge = VoiceBridge(
        emitState: { [weak self] state in
          self?.sendEvent("onVoiceState", ["state": state])
        },
        emitTranscript: { [weak self] text, final in
          self?.sendEvent("onVoiceTranscript", ["text": text, "final": final])
        },
        emitEnded: { [weak self] reason in
          self?.sendEvent("onVoiceEnded", ["reason": reason])
        }
      )

      // Held for the duration of the call. Go keeps its own proxy reference, but the only strong
      // reference on this side would otherwise fall out of scope the moment this function returns —
      // and it returns as soon as the call is *offered*, with the whole conversation still ahead.
      self.voiceBridge = bridge

      do {
        try wacli { MobileStartVoiceCall(to, relayURL, token, language, voice, bridge, $0) }
      } catch {
        // No call was placed, so OnEnded will never fire and nothing else will clear this.
        self.voiceBridge = nil
        throw error
      }
    }

    AsyncFunction("endVoiceCall") { (reason: String) throws in
      try wacli { MobileEndVoiceCall(reason, $0) }
    }

    // ---- session handover ----

    AsyncFunction("exportSession") { () throws -> String in
      guard let blob = try wacli({ MobileExportSession($0) }) else {
        throw NoSessionException()
      }
      return blob.base64EncodedString()
    }

    AsyncFunction("importSession") { (base64: String) throws in
      guard let blob = Data(base64Encoded: base64) else {
        throw InvalidBase64Exception("session")
      }
      try wacli { MobileImportSession(blob, $0) }
    }

    AsyncFunction("hasSession") { () -> Bool in
      MobileHasSession()
    }

    AsyncFunction("releaseSession") { () throws in
      try wacli { MobileReleaseSession($0) }
    }

    OnAppEntersBackground {
      // A login in progress is not a session to be tidied away. Pairing by code is typed into
      // WhatsApp on this same phone, so leaving the app is a step inside the flow — and the socket
      // being stopped here is the one the pairing completes over. Keep it up and ask iOS for as
      // long as it will give us, which is the whole budget the user has to go and type the code.
      if MobileIsLoggingIn() {
        self.beginLoginGrace()
        return
      }
      self.shouldResumeOnForeground = MobileIsRunning()
      if self.shouldResumeOnForeground {
        // Errors here are not actionable — the app is on its way out — but leaving the databases
        // open into a freeze is what corrupts them, so the attempt matters.
        try? wacli { MobileStop($0) }
      }
    }

    OnAppEntersForeground {
      self.endLoginGrace()
      // Starting under a login would fail anyway — MobileStart refuses without a stored session,
      // and the pairing that would create one has not finished.
      guard !MobileIsLoggingIn() else { return }
      guard self.shouldResumeOnForeground, !MobileIsRunning() else { return }
      self.shouldResumeOnForeground = false
      do {
        try wacli { MobileStart($0) }
        // Tell JS the connection was re-established so it can refresh: anything that arrived while
        // the app was away is in the database now, not in the event stream it was listening to.
        self.sendEvent("onEvent", [
          "event": "connection_state",
          "payload": #"{"state":"resumed"}"#,
        ])
      } catch {
        self.sendEvent("onLoginError", ["message": "resume failed: \(error.localizedDescription)"])
      }
    }
  }

  /**
   Ask iOS not to suspend us while the user is in WhatsApp typing a pairing code.

   This buys the grace period the system is willing to grant a departing app — around thirty
   seconds — and no more. There is no entitlement that would extend it, so a user who goes hunting
   through WhatsApp's menus can still be suspended mid-pairing and has to start again. It is the
   difference between "usually works if you know where you are going" and "cannot work at all",
   which is what stopping the service outright made it.

   The expiry handler is not optional: iOS kills the app outright if an assertion is still held when
   the time runs out.
   */
  private func beginLoginGrace() {
    guard loginGrace == .invalid else { return }
    loginGrace = UIApplication.shared.beginBackgroundTask(withName: "wacli.login") { [weak self] in
      self?.endLoginGrace()
    }
  }

  private func endLoginGrace() {
    guard loginGrace != .invalid else { return }
    UIApplication.shared.endBackgroundTask(loginGrace)
    loginGrace = .invalid
  }

  /**
   Point Go at the app's Application Support directory.

   iOS has no home directory for wacli's usual `~/.wacli` to resolve against, so this has to happen
   before any other call. Application Support is inside the app container, excluded from the
   user-visible Files app, and — unlike Caches — never reclaimed by the system under storage
   pressure, which matters when the directory holds the only copy of the session keys.
   */
  private func ensureConfigured() throws {
    guard !configured else { return }
    let base = try FileManager.default.url(
      for: .applicationSupportDirectory,
      in: .userDomainMask,
      appropriateFor: nil,
      create: true
    )
    let home = base.appendingPathComponent("wacli", isDirectory: true)
    try FileManager.default.createDirectory(at: home, withIntermediateDirectories: true)
    try wacli { MobileConfigure(home.path, $0) }
    configured = true
  }

  private func loginBridge() -> LoginBridge {
    LoginBridge(
      onQR: { [weak self] code in self?.sendEvent("onLoginQRCode", ["code": code]) },
      onPairing: { [weak self] code in self?.sendEvent("onLoginPairingCode", ["code": code]) },
      onStatus: { [weak self] status in self?.sendEvent("onLoginStatus", ["status": status]) },
      onError: { [weak self] message in self?.sendEvent("onLoginError", ["message": message]) }
    )
  }
}

/**
 Call a gomobile binding that reports failure through a trailing `NSError**`.

 gobind emits these as free C functions carrying no `swift_error` annotation, so Swift does not fold
 the error parameter into `throws` the way it does for Objective-C methods — the pointer has to be
 passed explicitly and the result checked by hand. Wrapping that here keeps the detail in one place
 instead of at twelve call sites.
 */
@discardableResult
private func wacli<T>(_ body: (NSErrorPointer) -> T) throws -> T {
  var error: NSError?
  let result = body(&error)
  if let error { throw error }
  return result
}

/// Adapts the Go `EventHandler` protocol to a Swift closure.
///
/// `MobileEventHandlerProtocol`, not `MobileEventHandler`: gobind emits both a protocol and a class
/// under that one Objective-C name, and Swift, which cannot have both, renames the protocol. Naming
/// the class here instead reads as a second superclass.
private class EventBridge: NSObject, MobileEventHandlerProtocol {
  private let handler: (String, String) -> Void

  init(_ handler: @escaping (String, String) -> Void) {
    self.handler = handler
  }

  func onEvent(_ event: String?, payloadJSON: String?) {
    handler(event ?? "", payloadJSON ?? "{}")
  }
}

/// Adapts the Go `LoginHandler` protocol to Swift closures.
private class LoginBridge: NSObject, MobileLoginHandlerProtocol {
  private let onQR: (String) -> Void
  private let onPairing: (String) -> Void
  private let onStatus: (String) -> Void
  private let onError: (String) -> Void

  init(
    onQR: @escaping (String) -> Void,
    onPairing: @escaping (String) -> Void,
    onStatus: @escaping (String) -> Void,
    onError: @escaping (String) -> Void
  ) {
    self.onQR = onQR
    self.onPairing = onPairing
    self.onStatus = onStatus
    self.onError = onError
  }

  func onQRCode(_ code: String?) { onQR(code ?? "") }
  func onPairingCode(_ code: String?) { onPairing(code ?? "") }
  func onStatus(_ status: String?) { onStatus(status ?? "") }
  func onError(_ message: String?) { onError(message ?? "unknown error") }
}

/// Adapts the Go `VoiceHandler` protocol to Swift closures.
///
/// The closures are named `emit…` rather than `on…` on purpose. A stored property and a protocol
/// method sharing a base name puts both in scope inside the method body, and the call then resolves
/// on argument type alone — `String` to the closure, `String?` to the method. It happens to pick the
/// closure, but the alternative is silent infinite recursion, and nothing at the call site shows
/// which one won. Different names make the question not arise.
private class VoiceBridge: NSObject, MobileVoiceHandlerProtocol {
  private let emitState: (String) -> Void
  private let emitTranscript: (String, Bool) -> Void
  private let emitEnded: (String) -> Void

  init(
    emitState: @escaping (String) -> Void,
    emitTranscript: @escaping (String, Bool) -> Void,
    emitEnded: @escaping (String) -> Void
  ) {
    self.emitState = emitState
    self.emitTranscript = emitTranscript
    self.emitEnded = emitEnded
  }

  func onState(_ state: String?) { emitState(state ?? "") }
  func onTranscript(_ text: String?, final: Bool) { emitTranscript(text ?? "", final) }
  func onEnded(_ reason: String?) { emitEnded(reason ?? "ended") }
}

internal final class InvalidBase64Exception: GenericException<String> {
  override var reason: String { "\(param) is not valid base64" }
}

internal final class NoSessionException: Exception {
  override var reason: String { "there is no session on this device to export" }
}
