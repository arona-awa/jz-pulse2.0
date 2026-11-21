package top.jzwall.pulse;

import android.content.Context;
import android.content.SharedPreferences;

import org.json.JSONArray;
import org.json.JSONObject;

import java.nio.charset.StandardCharsets;

final class EventPoller {
    private static final Object LOCK = new Object();
    private static final String PREFS = "jz_events";
    private static final String KEY_CURSOR = "cursor";
    private static final String KEY_USER = "user_id";

    static void poll(Context context) {
        synchronized (LOCK) {
            try {
                if (!SiteClient.hasLogin(context)) return;
                SharedPreferences sp = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE);
                long cursor = sp.getLong(KEY_CURSOR, -1L);
                int storedUser = sp.getInt(KEY_USER, 0);
                if (cursor < 0) {
                    bootstrap(context, sp);
                    return;
                }

                JSONObject page = fetch(context, SiteConfig.EVENTS_URL + "?after=" + cursor + "&limit=20");
                if (page == null) return;
                int serverUser = page.optInt("user_id", 0);
                if (serverUser == 0 || (storedUser != 0 && serverUser != storedUser)) {
                    bootstrap(context, sp);
                    return;
                }

                JSONArray events = page.optJSONArray("events");
                if (events != null) {
                    for (int i = 0; i < events.length(); i++) {
                        JSONObject event = events.optJSONObject(i);
                        if (event == null) continue;
                        NotificationHelper.showEvent(context, event);
                    }
                }
                long newCursor = page.optLong("cursor", cursor);
                sp.edit().putLong(KEY_CURSOR, newCursor).putInt(KEY_USER, serverUser).apply();
            } catch (Exception ignored) {
            }
        }
    }

    private static void bootstrap(Context context, SharedPreferences sp) {
        JSONObject page = fetch(context, SiteConfig.EVENTS_URL + "?latest=1");
        if (page == null) return;
        int serverUser = page.optInt("user_id", 0);
        if (serverUser == 0) return;
        sp.edit()
                .putLong(KEY_CURSOR, page.optLong("cursor", 0))
                .putInt(KEY_USER, serverUser)
                .apply();
    }

    private static JSONObject fetch(Context context, String url) {
        SiteClient.Response response = SiteClient.fetch(context, url, "application/json");
        if (response == null || response.code != 200 || response.body.length == 0) return null;
        try {
            return new JSONObject(new String(response.body, StandardCharsets.UTF_8));
        } catch (Exception e) {
            return null;
        }
    }

    private EventPoller() {
    }
}
