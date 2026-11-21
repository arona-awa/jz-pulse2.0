package top.jzwall.pulse;

import android.content.Context;
import android.webkit.CookieManager;
import android.webkit.WebSettings;

import java.io.ByteArrayOutputStream;
import java.io.InputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.util.List;
import java.util.Map;

final class SiteClient {
    static final class Response {
        final int code;
        final String contentType;
        final byte[] body;

        Response(int code, String contentType, byte[] body) {
            this.code = code;
            this.contentType = contentType;
            this.body = body;
        }
    }

    static Response fetch(Context context, String url, String accept) {
        HttpURLConnection conn = null;
        try {
            conn = (HttpURLConnection) new URL(url).openConnection();
            conn.setConnectTimeout(10000);
            conn.setReadTimeout(15000);
            conn.setRequestMethod("GET");
            conn.setInstanceFollowRedirects(true);
            conn.setRequestProperty("Accept-Encoding", "identity");
            conn.setRequestProperty("User-Agent", WebSettings.getDefaultUserAgent(context) + " JZPulseApp/1.0");
            if (accept != null && !accept.isEmpty()) {
                conn.setRequestProperty("Accept", accept);
            }
            String cookie = CookieManager.getInstance().getCookie(url);
            if (cookie != null && !cookie.isEmpty()) {
                conn.setRequestProperty("Cookie", cookie);
            }
            int code = conn.getResponseCode();
            String contentType = conn.getContentType();
            saveCookies(conn, url);
            InputStream in = code >= 400 ? conn.getErrorStream() : conn.getInputStream();
            byte[] body = readAll(in, 8 * 1024 * 1024);
            return new Response(code, contentType, body);
        } catch (Exception e) {
            return null;
        } finally {
            if (conn != null) conn.disconnect();
        }
    }

    static boolean hasLogin(Context context) {
        String user = cookieValue("clv_user");
        return user != null && !user.isEmpty();
    }

    static String cacheKey(Context context, String url) {
        String user = cookieValue("clv_user");
        if (user == null || user.isEmpty()) user = "anon";
        return url + "|" + sha256(user).substring(0, 24);
    }

    private static String cookieValue(String name) {
        try {
            String all = CookieManager.getInstance().getCookie(SiteConfig.BASE_URL);
            if (all == null) return null;
            for (String part : all.split(";")) {
                String p = part.trim();
                int eq = p.indexOf('=');
                if (eq > 0 && p.substring(0, eq).equals(name)) {
                    return p.substring(eq + 1);
                }
            }
        } catch (Exception ignored) {
        }
        return null;
    }

    private static void saveCookies(HttpURLConnection conn, String url) {
        try {
            CookieManager cm = CookieManager.getInstance();
            Map<String, List<String>> headers = conn.getHeaderFields();
            for (Map.Entry<String, List<String>> e : headers.entrySet()) {
                if (e.getKey() == null || !"Set-Cookie".equalsIgnoreCase(e.getKey())) continue;
                for (String v : e.getValue()) cm.setCookie(url, v);
            }
            cm.flush();
        } catch (Exception ignored) {
        }
    }

    private static byte[] readAll(InputStream in, int max) throws Exception {
        if (in == null) return new byte[0];
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        byte[] buf = new byte[8192];
        int n;
        while ((n = in.read(buf)) > 0) {
            out.write(buf, 0, n);
            if (out.size() > max) break;
        }
        in.close();
        return out.toByteArray();
    }

    private static String sha256(String value) {
        try {
            MessageDigest md = MessageDigest.getInstance("SHA-256");
            byte[] d = md.digest(value.getBytes(StandardCharsets.UTF_8));
            StringBuilder sb = new StringBuilder();
            for (byte b : d) sb.append(String.format("%02x", b));
            return sb.toString();
        } catch (Exception e) {
            return Integer.toHexString(value.hashCode());
        }
    }

    private SiteClient() {
    }
}
