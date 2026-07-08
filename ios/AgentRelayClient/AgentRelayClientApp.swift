import SwiftUI
import SwiftData
import UIKit
import WebKit

@main
struct AgentRelayClientApp: App {
    // Standard model container for SwiftData persistence
    let container: ModelContainer
    
    init() {
        do {
            // Configure schema and storage settings
            let schema = Schema([HostProfile.self])
            let config = ModelConfiguration(schema: schema, isStoredInMemoryOnly: false)
            self.container = try ModelContainer(for: schema, configurations: [config])
            
            // Seed default profile entries if database is completely empty on first launch
            seedDefaultProfilesIfNeeded()
        } catch {
            fatalError("Failed to initialize SwiftData ModelContainer: \(error)")
        }
    }
    
    var body: some Scene {
        WindowGroup {
            // The app now behaves like a focused Safari-style browser onto the
            // hosted relay web UI: BrowserView loads this URL on launch and keeps
            // the AppleConnect session across relaunches. (The native
            // MainSplitView client stays in the project — it's just no longer the
            // entry point; swap it back here to restore the native UI.)
            BrowserView(url: URL(string: "https://code.sqeqct.ops.apple.com")!)
                .ignoresSafeArea()
                .modelContainer(container)
                .preferredColorScheme(.dark) // Dark-mode first design
        }
    }
    
    // MARK: - Database Seeding Helper
    
    @MainActor
    private func seedDefaultProfilesIfNeeded() {
        let context = container.mainContext
        let fetchDescriptor = FetchDescriptor<HostProfile>()
        
        do {
            let count = try context.fetchCount(fetchDescriptor)
            if count == 0 {
                // Seed local and remote development hubs
                let localRelay = HostProfile(
                    name: "Local Host Laptop",
                    hostUrlString: "http://localhost:8080",
                    isDefault: false
                )
                
                let communityRelay = HostProfile(
                    name: "SQE Agent Relay",
                    hostUrlString: "https://code.sqeqct.ops.apple.com",
                    isDefault: true
                )
                
                context.insert(localRelay)
                context.insert(communityRelay)
                try context.save()
                print("Successfully seeded default host connection profiles into SwiftData.")
            }
        } catch {
            print("Failed to seed default database profiles: \(error)")
        }
    }
}

// MARK: - Safari-like Web Browser Root

// BrowserView turns the app into a focused, Safari-style web browser: a
// full-screen WKWebView that loads `url` on launch, persists cookies/login
// across relaunches, follows the relay's AppleConnect redirects, and offers the
// usual browser affordances — swipe back/forward, pull-to-refresh, a top
// progress bar, and opening target=_blank links in place. It accepts the
// Apple-internal host's TLS exactly like SSLTrustDelegate / AppleConnectWebView,
// so https://code.sqeqct.ops.apple.com loads without a certificate error.
struct BrowserView: UIViewControllerRepresentable {
    let url: URL

    func makeCoordinator() -> Coordinator { Coordinator(url: url) }

    func makeUIViewController(context: Context) -> UIViewController {
        let vc = UIViewController()
        vc.view.backgroundColor = .systemBackground

        let config = WKWebViewConfiguration()
        config.websiteDataStore = .default()       // persistent cookie store → session survives relaunch
        config.allowsInlineMediaPlayback = true

        let webView = WKWebView(frame: .zero, configuration: config)
        webView.navigationDelegate = context.coordinator
        webView.uiDelegate = context.coordinator
        webView.allowsBackForwardNavigationGestures = true
        webView.isOpaque = false                   // avoid a white flash before dark content paints
        webView.backgroundColor = .systemBackground
        webView.scrollView.backgroundColor = .systemBackground
        webView.translatesAutoresizingMaskIntoConstraints = false
        vc.view.addSubview(webView)

        let progress = UIProgressView(progressViewStyle: .bar)
        progress.translatesAutoresizingMaskIntoConstraints = false
        progress.trackTintColor = .clear
        progress.alpha = 0
        vc.view.addSubview(progress)

        let spinner = UIActivityIndicatorView(style: .large)
        spinner.translatesAutoresizingMaskIntoConstraints = false
        spinner.hidesWhenStopped = true
        vc.view.addSubview(spinner)

        let errorLabel = UILabel()
        errorLabel.translatesAutoresizingMaskIntoConstraints = false
        errorLabel.numberOfLines = 0
        errorLabel.textAlignment = .center
        errorLabel.textColor = .secondaryLabel
        errorLabel.font = .preferredFont(forTextStyle: .callout)
        errorLabel.isHidden = true
        vc.view.addSubview(errorLabel)

        let guide = vc.view.safeAreaLayoutGuide
        NSLayoutConstraint.activate([
            webView.topAnchor.constraint(equalTo: guide.topAnchor),
            webView.bottomAnchor.constraint(equalTo: guide.bottomAnchor),
            webView.leadingAnchor.constraint(equalTo: guide.leadingAnchor),
            webView.trailingAnchor.constraint(equalTo: guide.trailingAnchor),
            progress.topAnchor.constraint(equalTo: guide.topAnchor),
            progress.leadingAnchor.constraint(equalTo: guide.leadingAnchor),
            progress.trailingAnchor.constraint(equalTo: guide.trailingAnchor),
            spinner.centerXAnchor.constraint(equalTo: vc.view.centerXAnchor),
            spinner.centerYAnchor.constraint(equalTo: vc.view.centerYAnchor),
            errorLabel.centerXAnchor.constraint(equalTo: vc.view.centerXAnchor),
            errorLabel.centerYAnchor.constraint(equalTo: vc.view.centerYAnchor),
            errorLabel.leadingAnchor.constraint(equalTo: vc.view.leadingAnchor, constant: 32),
            errorLabel.trailingAnchor.constraint(equalTo: vc.view.trailingAnchor, constant: -32),
        ])

        let refresh = UIRefreshControl()
        refresh.addTarget(context.coordinator, action: #selector(Coordinator.reload), for: .valueChanged)
        webView.scrollView.refreshControl = refresh

        context.coordinator.webView = webView
        context.coordinator.progress = progress
        context.coordinator.spinner = spinner
        context.coordinator.errorLabel = errorLabel
        context.coordinator.refresh = refresh
        context.coordinator.startObservingProgress()

        webView.load(URLRequest(url: url))
        return vc
    }

    func updateUIViewController(_ uiViewController: UIViewController, context: Context) {}

    final class Coordinator: NSObject, WKNavigationDelegate, WKUIDelegate {
        let url: URL
        weak var webView: WKWebView?
        weak var progress: UIProgressView?
        weak var spinner: UIActivityIndicatorView?
        weak var errorLabel: UILabel?
        weak var refresh: UIRefreshControl?
        private var progressObservation: NSKeyValueObservation?

        init(url: URL) { self.url = url }
        deinit { progressObservation?.invalidate() }

        @objc func reload() { webView?.reload() }

        func startObservingProgress() {
            progressObservation = webView?.observe(\.estimatedProgress, options: [.new]) { [weak self] webView, _ in
                guard let bar = self?.progress else { return }
                let value = Float(webView.estimatedProgress)
                bar.setProgress(value, animated: true)
                if value >= 1 {
                    UIView.animate(withDuration: 0.25, delay: 0.1, options: []) {
                        bar.alpha = 0
                    } completion: { _ in
                        bar.setProgress(0, animated: false)
                    }
                } else {
                    bar.alpha = 1
                }
            }
        }

        // Accept the relay's / IdMS's server trust for Apple-internal hosts,
        // mirroring SSLTrustDelegate; otherwise the internal cert fails the load.
        func webView(_ webView: WKWebView, didReceive challenge: URLAuthenticationChallenge, completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
            if challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust,
               let trust = challenge.protectionSpace.serverTrust {
                let host = challenge.protectionSpace.host
                if host.contains("apple.com") || host.contains("localhost") || host.contains("127.0.0.1") {
                    completionHandler(.useCredential, URLCredential(trust: trust))
                    return
                }
            }
            completionHandler(.performDefaultHandling, nil)
        }

        // Keep target=_blank / window.open navigations in this same web view
        // instead of dropping them (a WKWebView has no second window to open).
        func webView(_ webView: WKWebView, createWebViewWith configuration: WKWebViewConfiguration, for navigationAction: WKNavigationAction, windowFeatures: WKWindowFeatures) -> WKWebView? {
            if navigationAction.targetFrame == nil, let target = navigationAction.request.url {
                webView.load(URLRequest(url: target))
            }
            return nil
        }

        func webView(_ webView: WKWebView, didStartProvisionalNavigation navigation: WKNavigation!) {
            errorLabel?.isHidden = true
            if refresh?.isRefreshing != true { spinner?.startAnimating() }
        }

        func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
            spinner?.stopAnimating()
            refresh?.endRefreshing()
        }

        func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: Error) {
            showError(error)
        }

        func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) {
            showError(error)
        }

        private func showError(_ error: Error) {
            let ns = error as NSError
            // A cancelled load is benign — it's what a server-side redirect looks like.
            if ns.domain == NSURLErrorDomain && ns.code == NSURLErrorCancelled { return }
            spinner?.stopAnimating()
            refresh?.endRefreshing()
            errorLabel?.text = "Couldn't load \(url.host ?? "the page").\n\n\(error.localizedDescription)\n\nPull down to try again."
            errorLabel?.isHidden = false
        }
    }
}
