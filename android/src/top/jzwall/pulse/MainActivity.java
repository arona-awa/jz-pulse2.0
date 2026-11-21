package top.jzwall.pulse;

import android.Manifest;
import android.app.Activity;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.graphics.Color;
import android.net.ConnectivityManager;
import android.net.Network;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.view.Gravity;
import android.view.View;
import android.view.ViewGroup;
import android.webkit.CookieManager;
import android.webkit.ValueCallback;
import android.webkit.WebChromeClient;
import android.webkit.WebResourceError;
import android.webkit.WebResourceRequest;
import android.webkit.WebResourceResponse;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.FrameLayout;
import android.widget.TextView;

import java.io.ByteArrayInputStream;
import java.io.ByteArrayOutputStream;
import java.nio.charset.StandardCharsets;
import java.util.HashMap;
import java.util.Locale;
import java.util.Map;
import java.util.concurrent.Executors;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.TimeUnit;

public class MainActivity extends Activity {
    private static final int REQ_FILE = 1001;
    private static final int REQ_NOTIFY = 1002;

    private WebView webView;
    private TextView offlineBar;
    private SiteCache cache;
    private ValueCallback<Uri[]> fileChooser;
    private ConnectivityManager.NetworkCallback networkCallback;
    private ScheduledExecutorService poller;
    private boolean pageIsOffline = false;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        getWindow().setStatusBarColor(Color.rgb(14, 17, 22));
        getWindow().setNavigationBarColor(Color.rgb(14, 17, 22));

        NotificationHelper.ensureChannel(this);
        NotificationJobService.schedule(this);
        requestNotificationPermission();

        cache = new SiteCache(this);
        buildUi();
        registerNetworkMonitor();
        handleIntent(getIntent());
        updateNetworkUi();
    }

    private void buildUi() {
        FrameLayout root = new FrameLayout(this);
        root.setBackgroundColor(Color.rgb(14, 17, 22));

        webView = new WebView(this);
        webView.setBackgroundColor(Color.rgb(14, 17, 22));
        WebSettings settings = webView.getSettings();
        settings.setJavaScriptEnabled(true);
        settings.setDomStorageEnabled(true);
        settings.setDatabaseEnabled(true);
        settings.setCacheMode(WebSettings.LOAD_DEFAULT);
        settings.setLoadWithOverviewMode(true);
        settings.setUseWideViewPort(true);
        settings.setAllowFileAccess(false);
        settings.setAllowContentAccess(false);
        settings.setMediaPlaybackRequiresUserGesture(true);
        settings.setSupportMultipleWindows(false);
        settings.setMixedContentMode(WebSettings.MIXED_CONTENT_NEVER_ALLOW);
        settings.setUserAgentString(settings.getUserAgentString() + " JZPulseApp/1.0");

        CookieManager.getInstance().setAcceptCookie(true);

        webView.setWebViewClient(new SiteWebViewClient());
        webView.setWebChromeClient(new SiteChromeClient());

        root.addView(webView, new FrameLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT));

        offlineBar = new TextView(this);
        offlineBar.setText(R.string.offline_banner);
        offlineBar.setTextColor(Color.WHITE);
        offlineBar.setTextSize(12);
        offlineBar.setGravity(Gravity.CENTER_VERTICAL);
        offlineBar.setPadding(dp(12), 0, dp(12), 0);
        offlineBar.setBackgroundColor(Color.rgb(179, 38, 30));
        offlineBar.setVisibility(View.GONE);
        FrameLayout.LayoutParams barParams = new FrameLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, dp(30));
        barParams.gravity = Gravity.TOP;
        root.addView(offlineBar, barParams);

        setContentView(root);
    }

    private void handleIntent(Intent intent) {
        String url = intent == null ? null : intent.getStringExtra("open_url");
        if (url == null || url.isEmpty()) url = SiteConfig.BASE_URL + "/";
        webView.loadUrl(url);
    }

    @Override
    protected void onNewIntent(Intent intent) {
        super.onNewIntent(intent);
        setIntent(intent);
        handleIntent(intent);
    }

    @Override
    protected void onResume() {
        super.onResume();
        updateNetworkUi();
        startForegroundPolling();
    }

    @Override
    protected void onPause() {
        stopForegroundPolling();
        try {
            CookieManager.getInstance().flush();
        } catch (Exception ignored) {
        }
        super.onPause();
    }

    @Override
    protected void onDestroy() {
        stopForegroundPolling();
        if (networkCallback != null) {
            try {
                ((ConnectivityManager) getSystemService(CONNECTIVITY_SERVICE))
                        .unregisterNetworkCallback(networkCallback);
            } catch (Exception ignored) {
            }
        }
        if (webView != null) {
            webView.loadUrl("about:blank");
            webView.destroy();
        }
        super.onDestroy();
    }

    @Override
    public void onBackPressed() {
        if (webView != null && webView.canGoBack()) {
            webView.goBack();
        } else {
            super.onBackPressed();
        }
    }

    private void startForegroundPolling() {
        if (poller != null) return;
        poller = Executors.newSingleThreadScheduledExecutor();
        poller.scheduleWithFixedDelay(new Runnable() {
            @Override
            public void run() {
                try {
                    EventPoller.poll(getApplicationContext());
                } catch (Exception ignored) {
                }
            }
        }, 4, 30, TimeUnit.SECONDS);
    }

    private void stopForegroundPolling() {
        if (poller != null) {
            poller.shutdownNow();
            poller = null;
        }
    }

    private void requestNotificationPermission() {
        if (Build.VERSION.SDK_INT >= 33 &&
                checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(new String[]{Manifest.permission.POST_NOTIFICATIONS}, REQ_NOTIFY);
        }
    }

    private void registerNetworkMonitor() {
        try {
            ConnectivityManager cm = (ConnectivityManager) getSystemService(CONNECTIVITY_SERVICE);
            networkCallback = new ConnectivityManager.NetworkCallback() {
                @Override
                public void onAvailable(Network network) {
                    runOnUiThread(new Runnable() {
                        @Override
                        public void run() {
                            updateNetworkUi();
                        }
                    });
                }

                @Override
                public void onLost(Network network) {
                    runOnUiThread(new Runnable() {
                        @Override
                        public void run() {
                            updateNetworkUi();
                        }
                    });
                }
            };
            cm.registerDefaultNetworkCallback(networkCallback);
        } catch (Exception ignored) {
        }
    }

    private void updateNetworkUi() {
        boolean online = NetworkState.isOnline(this);
        pageIsOffline = !online;
        if (offlineBar != null) offlineBar.setVisibility(online ? View.GONE : View.VISIBLE);
        if (webView != null) {
            webView.getSettings().setCacheMode(online
                    ? WebSettings.LOAD_DEFAULT : WebSettings.LOAD_CACHE_ELSE_NETWORK);
        }
    }

    private WebResourceResponse intercept(WebResourceRequest request) {
        try {
            if (request == null || request.getUrl() == null) return null;
            if (request.getMethod() != null && !"GET".equalsIgnoreCase(request.getMethod())) return null;
            Uri uri = request.getUrl();
            if (!SiteConfig.isSiteHost(uri.getHost())) return null;

            String path = uri.getPath() == null ? "/" : uri.getPath();
            boolean api = path.startsWith("/api/");
            boolean mainFrame = request.isForMainFrame();
            boolean html = mainFrame || acceptsHtml(request);
            if (!api && !html) return null;

            // 一次性验证码和原生通知接口不进入 WebView 缓存。
            if (api && (path.startsWith("/api/captcha") || path.startsWith("/api/v1/events"))) return null;

            String key = SiteClient.cacheKey(this, uri.toString());
            if (!NetworkState.isOnline(this)) {
                SiteCache.Entry cached = cache.get(key);
                if (cached != null) return response(cached.body, cached.contentType, 200);
                return offlineResponse(api);
            }

            SiteClient.Response fetched = SiteClient.fetch(this, uri.toString(),
                    api ? "application/json" : "text/html");
            if (fetched == null) {
                SiteCache.Entry cached = cache.get(key);
                if (cached != null) return response(cached.body, cached.contentType, 200);
                return offlineResponse(api);
            }
            if (fetched.code == 200 && fetched.body.length > 0) {
                cache.put(key, fetched.contentType, fetched.body);
            }
            return response(fetched.body, fetched.contentType, fetched.code);
        } catch (Exception e) {
            return offlineResponse(false);
        }
    }

    private boolean acceptsHtml(WebResourceRequest request) {
        try {
            Map<String, String> headers = request.getRequestHeaders();
            if (headers == null) return false;
            for (Map.Entry<String, String> entry : headers.entrySet()) {
                if (entry.getKey() != null && "accept".equalsIgnoreCase(entry.getKey()) &&
                        entry.getValue() != null && entry.getValue().contains("text/html")) {
                    return true;
                }
            }
        } catch (Exception ignored) {
        }
        return false;
    }

    private WebResourceResponse response(byte[] body, String contentType, int code) {
        if (body == null) body = new byte[0];
        String mime = mimeOf(contentType);
        Map<String, String> headers = new HashMap<>();
        headers.put("Cache-Control", "no-store");
        headers.put("X-JZ-App", "1");
        int status = code <= 0 ? 200 : code;
        return new WebResourceResponse(mime, "UTF-8", status,
                status == 200 ? "OK" : "Error", headers, new ByteArrayInputStream(body));
    }

    private WebResourceResponse offlineResponse(boolean api) {
        Map<String, String> headers = new HashMap<>();
        headers.put("Cache-Control", "no-store");
        headers.put("X-JZ-App", "1");
        if (api) {
            byte[] body = "{\"ok\":false,\"msg\":\"当前离线，暂无缓存数据\"}"
                    .getBytes(StandardCharsets.UTF_8);
            return new WebResourceResponse("application/json", "UTF-8", 200,
                    "OK", headers, new ByteArrayInputStream(body));
        }
        String html = "<!doctype html><html><head><meta charset=\"utf-8\">" +
                "<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">" +
                "<style>body{margin:0;background:#0e1116;color:#e8eaf0;font-family:sans-serif;" +
                "display:flex;min-height:100vh;align-items:center;justify-content:center;text-align:center}" +
                "h1{font-size:20px}p{color:#9aa3b2;font-size:14px;line-height:1.7}</style></head>" +
                "<body><div><h1>姜中Pulse</h1><p>当前处于离线状态<br>暂无可用缓存，请联网后重新打开</p></div></body></html>";
        return new WebResourceResponse("text/html", "UTF-8", 200, "OK", headers,
                new ByteArrayInputStream(html.getBytes(StandardCharsets.UTF_8)));
    }

    private String mimeOf(String contentType) {
        if (contentType == null || contentType.isEmpty()) return "text/plain";
        int semi = contentType.indexOf(';');
        String mime = semi >= 0 ? contentType.substring(0, semi) : contentType;
        mime = mime.trim().toLowerCase(Locale.ROOT);
        return mime.isEmpty() ? "text/plain" : mime;
    }

    private int dp(int value) {
        return Math.round(value * getResources().getDisplayMetrics().density);
    }

    private final class SiteWebViewClient extends WebViewClient {
        @Override
        public WebResourceResponse shouldInterceptRequest(WebView view, WebResourceRequest request) {
            return intercept(request);
        }

        @Override
        public boolean shouldOverrideUrlLoading(WebView view, WebResourceRequest request) {
            Uri uri = request.getUrl();
            if (uri != null && SiteConfig.isSiteHost(uri.getHost())) return false;
            try {
                startActivity(new Intent(Intent.ACTION_VIEW, uri));
            } catch (Exception ignored) {
            }
            return true;
        }

        @Override
        public void onPageFinished(WebView view, String url) {
            updateNetworkUi();
        }

        @Override
        public void onReceivedError(WebView view, WebResourceRequest request, WebResourceError error) {
            if (request != null && request.isForMainFrame()) {
                pageIsOffline = true;
                if (offlineBar != null) offlineBar.setVisibility(View.VISIBLE);
            }
        }
    }

    private final class SiteChromeClient extends WebChromeClient {
        @Override
        public boolean onShowFileChooser(WebView view, ValueCallback<Uri[]> callback,
                                         FileChooserParams params) {
            if (fileChooser != null) fileChooser.onReceiveValue(null);
            fileChooser = callback;
            try {
                Intent intent = params.createIntent();
                intent.addCategory(Intent.CATEGORY_OPENABLE);
                intent.putExtra(Intent.EXTRA_ALLOW_MULTIPLE, true);
                startActivityForResult(Intent.createChooser(intent, "选择文件"), REQ_FILE);
                return true;
            } catch (Exception e) {
                fileChooser = null;
                return false;
            }
        }
    }

    @Override
    protected void onActivityResult(int requestCode, int resultCode, Intent data) {
        super.onActivityResult(requestCode, resultCode, data);
        if (requestCode == REQ_FILE && fileChooser != null) {
            Uri[] result = null;
            if (resultCode == RESULT_OK) {
                result = WebChromeClient.FileChooserParams.parseResult(resultCode, data);
            }
            fileChooser.onReceiveValue(result);
            fileChooser = null;
        }
    }
}
