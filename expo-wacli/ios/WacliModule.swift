import ExpoModulesCore
import Mobile

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

  public func definition() -> ModuleDefinition {
    Name("Wacli")

    Events(
      "onEvent",
      "onLoginQRCode",
      "onLoginPairingCode",
      "onLoginStatus",
      "onLoginError"
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

    OnAppEntersBackground {
      self.shouldResumeOnForeground = MobileIsRunning()
      if self.shouldResumeOnForeground {
        // Errors here are not actionable — the app is on its way out — but leaving the databases
        // open into a freeze is what corrupts them, so the attempt matters.
        try? wacli { MobileStop($0) }
      }
    }

    OnAppEntersForeground {
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
