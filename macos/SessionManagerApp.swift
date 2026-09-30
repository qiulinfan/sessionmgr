import AppKit
import WebKit

@MainActor
final class SessionManagerDelegate: NSObject, NSApplicationDelegate, WKNavigationDelegate, WKUIDelegate {
    private var window: NSWindow!
    private var webView: WKWebView!
    private let backend = Process()
    private let output = Pipe()
    private var outputBuffer = Data()
    private var serverURL: URL?
    private var startupTimeout: DispatchWorkItem?
    private var terminating = false
    private var reportedFailure = false

    func applicationDidFinishLaunching(_ notification: Notification) {
        configureMenu()
        window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 1100, height: 860),
            styleMask: [.titled, .closable, .miniaturizable, .resizable],
            backing: .buffered,
            defer: false
        )
        window.title = "Session Manager"
        window.minSize = NSSize(width: 720, height: 560)
        window.backgroundColor = NSColor(red: 13 / 255, green: 17 / 255, blue: 23 / 255, alpha: 1)
        window.setFrameAutosaveName("SessionManagerWindow")
        window.center()

        webView = WKWebView(frame: .zero)
        webView.navigationDelegate = self
        webView.uiDelegate = self
        webView.underPageBackgroundColor = window.backgroundColor
        window.contentView = webView
        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
        webView.loadHTMLString("""
            <!doctype html><html><body style="background:#0d1117;color:#8b949e;font:16px system-ui;padding:48px">
            Starting Session Manager…</body></html>
            """, baseURL: nil)
        startBackend()
    }

    private func configureMenu() {
        let menu = NSMenu()
        let applicationMenu = NSMenu()
        applicationMenu.addItem(withTitle: "About Session Manager", action: #selector(NSApplication.orderFrontStandardAboutPanel(_:)), keyEquivalent: "")
        let installCLI = applicationMenu.addItem(withTitle: "Install smg Command…", action: #selector(installCommandLineTool), keyEquivalent: "")
        installCLI.target = self
        applicationMenu.addItem(.separator())
        applicationMenu.addItem(withTitle: "Quit Session Manager", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q")
        let applicationItem = NSMenuItem()
        applicationItem.submenu = applicationMenu
        menu.addItem(applicationItem)

        let editMenu = NSMenu(title: "Edit")
        for (title, selector, key) in [("Cut", "cut:", "x"), ("Copy", "copy:", "c"), ("Paste", "paste:", "v"), ("Select All", "selectAll:", "a")] {
            editMenu.addItem(withTitle: title, action: Selector(selector), keyEquivalent: key)
        }
        let editItem = NSMenuItem(title: "Edit", action: nil, keyEquivalent: "")
        editItem.submenu = editMenu
        menu.addItem(editItem)

        let viewMenu = NSMenu(title: "View")
        let reload = viewMenu.addItem(withTitle: "Reload", action: #selector(reloadInterface), keyEquivalent: "r")
        reload.target = self
        let viewItem = NSMenuItem(title: "View", action: nil, keyEquivalent: "")
        viewItem.submenu = viewMenu
        menu.addItem(viewItem)
        NSApp.mainMenu = menu
    }

    private func startBackend() {
        guard let executable = Bundle.main.url(forResource: "sessionmgr", withExtension: nil, subdirectory: "bin") else {
            fail("The bundled Session Manager executable is missing.")
            return
        }
        backend.executableURL = executable
        backend.arguments = ["gui", "--no-open"] + Array(CommandLine.arguments.dropFirst())
        backend.currentDirectoryURL = FileManager.default.homeDirectoryForCurrentUser
        backend.standardInput = FileHandle.nullDevice
        backend.standardOutput = output
        backend.standardError = FileHandle.nullDevice
        output.fileHandleForReading.readabilityHandler = { [weak self] handle in
            let data = handle.availableData
            guard !data.isEmpty else {
                handle.readabilityHandler = nil
                return
            }
            DispatchQueue.main.async { self?.readStartupOutput(data) }
        }
        backend.terminationHandler = { [weak self] process in
            let status = process.terminationStatus
            DispatchQueue.main.async {
                guard let self, !self.terminating else { return }
                self.fail("The local Session Manager server stopped (exit status \(status)). Reopen the app to try again.")
            }
        }
        do {
            try backend.run()
        } catch {
            fail("The local Session Manager server could not be started.")
            return
        }
        let timeout = DispatchWorkItem { [weak self] in
            guard let self, self.serverURL == nil else { return }
            self.fail("The local Session Manager server did not become ready within 15 seconds.")
        }
        startupTimeout = timeout
        DispatchQueue.main.asyncAfter(deadline: .now() + 15, execute: timeout)
    }

    @objc private func installCommandLineTool() {
        guard let executable = Bundle.main.url(forResource: "sessionmgr", withExtension: nil, subdirectory: "bin") else { return }
        let installer = Process()
        let output = Pipe()
        installer.executableURL = executable
        installer.arguments = ["install-cli"]
        installer.standardOutput = output
        installer.standardError = output
        let alert = NSAlert()
        do {
            try installer.run()
            output.fileHandleForWriting.closeFile()
            let response = output.fileHandleForReading.readDataToEndOfFile()
            installer.waitUntilExit()
            alert.messageText = installer.terminationStatus == 0 ? "smg command installed" : "smg installation failed"
            alert.informativeText = String(decoding: response.prefix(8192), as: UTF8.self)
        } catch {
            alert.messageText = "smg installation failed"
            alert.informativeText = "The bundled installer could not be started."
        }
        output.fileHandleForReading.closeFile()
        alert.addButton(withTitle: "OK")
        alert.runModal()
    }

    private func readStartupOutput(_ data: Data) {
        guard serverURL == nil, !terminating else { return }
        outputBuffer.append(data)
        guard outputBuffer.count <= 64 * 1024 else {
            fail("The local server returned an invalid startup response.")
            return
        }
        while let newline = outputBuffer.firstIndex(of: 10) {
            let line = String(decoding: outputBuffer[..<newline], as: UTF8.self)
            outputBuffer.removeSubrange(...newline)
            let prefix = "Session Manager GUI: "
            guard line.hasPrefix(prefix) else { continue }
            guard let url = URL(string: String(line.dropFirst(prefix.count))),
                  url.scheme == "http", url.host == "127.0.0.1", url.port != nil,
                  url.path == "/", !(url.fragment ?? "").isEmpty else {
                fail("The local server returned an invalid interface address.")
                return
            }
            // The private readiness token stays in memory and the local webview;
            // it is never written to logs or opened in an external browser.
            serverURL = url
            startupTimeout?.cancel()
            outputBuffer.removeAll()
            webView.load(URLRequest(url: url))
            return
        }
    }

    private func isServerURL(_ url: URL) -> Bool {
        guard let serverURL else { return false }
        return url.scheme == serverURL.scheme && url.host == serverURL.host && url.port == serverURL.port
    }

    func webView(_ webView: WKWebView, decidePolicyFor navigationAction: WKNavigationAction,
                 decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
        guard let url = navigationAction.request.url else { decisionHandler(.cancel); return }
        if isServerURL(url) || (serverURL == nil && url.absoluteString == "about:blank") {
            decisionHandler(.allow)
        } else {
            if navigationAction.navigationType == .linkActivated && url.scheme == "https" {
                NSWorkspace.shared.open(url)
            }
            decisionHandler(.cancel)
        }
    }

    func webView(_ webView: WKWebView, createWebViewWith configuration: WKWebViewConfiguration,
                 for navigationAction: WKNavigationAction, windowFeatures: WKWindowFeatures) -> WKWebView? {
        if let url = navigationAction.request.url, url.scheme == "https" {
            NSWorkspace.shared.open(url)
        }
        return nil
    }

    func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) {
        if (error as NSError).code != NSURLErrorCancelled {
            fail("The Session Manager interface could not load from its local server.")
        }
    }

    @objc private func reloadInterface() { webView.reload() }

    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        window.makeKeyAndOrderFront(nil)
        sender.activate(ignoringOtherApps: true)
        return true
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { true }

    func applicationWillTerminate(_ notification: Notification) {
        terminating = true
        startupTimeout?.cancel()
        output.fileHandleForReading.readabilityHandler = nil
        if backend.isRunning {
            backend.terminate()
            backend.waitUntilExit()
        }
    }

    private func fail(_ message: String) {
        guard !terminating, !reportedFailure else { return }
        reportedFailure = true
        let alert = NSAlert()
        alert.alertStyle = .critical
        alert.messageText = "Session Manager could not continue"
        alert.informativeText = message
        alert.addButton(withTitle: "Quit")
        alert.runModal()
        NSApp.terminate(nil)
    }
}

@main
struct SessionManagerApplication {
    @MainActor
    static func main() {
        let application = NSApplication.shared
        application.setActivationPolicy(.regular)
        let delegate = SessionManagerDelegate()
        application.delegate = delegate
        withExtendedLifetime(delegate) { application.run() }
    }
}
