package top.jzwall.pulse;

import android.content.ContentValues;
import android.content.Context;
import android.database.Cursor;
import android.database.sqlite.SQLiteDatabase;
import android.database.sqlite.SQLiteOpenHelper;

final class SiteCache extends SQLiteOpenHelper {
    static final class Entry {
        final String contentType;
        final byte[] body;

        Entry(String contentType, byte[] body) {
            this.contentType = contentType;
            this.body = body;
        }
    }

    private static final int MAX_BODY = 3 * 1024 * 1024;
    private static final long MAX_AGE = 30L * 24 * 60 * 60 * 1000;

    SiteCache(Context context) {
        super(context.getApplicationContext(), "jz_site_cache.db", null, 1);
    }

    @Override
    public void onCreate(SQLiteDatabase db) {
        db.execSQL("CREATE TABLE cache (cache_key TEXT PRIMARY KEY, content_type TEXT, body BLOB, updated_at INTEGER)");
        db.execSQL("CREATE INDEX idx_cache_updated ON cache(updated_at)");
    }

    @Override
    public void onUpgrade(SQLiteDatabase db, int oldVersion, int newVersion) {
        db.execSQL("DROP TABLE IF EXISTS cache");
        onCreate(db);
    }

    synchronized Entry get(String key) {
        Cursor c = null;
        try {
            c = getReadableDatabase().query("cache",
                    new String[]{"content_type", "body"},
                    "cache_key=?", new String[]{key}, null, null, null, "1");
            if (c.moveToFirst() && !c.isNull(1)) {
                return new Entry(c.getString(0), c.getBlob(1));
            }
        } catch (Exception ignored) {
        } finally {
            if (c != null) c.close();
        }
        return null;
    }

    synchronized void put(String key, String contentType, byte[] body) {
        if (body == null || body.length == 0 || body.length > MAX_BODY) return;
        try {
            ContentValues v = new ContentValues();
            v.put("cache_key", key);
            v.put("content_type", contentType == null ? "" : contentType);
            v.put("body", body);
            v.put("updated_at", System.currentTimeMillis());
            getWritableDatabase().insertWithOnConflict("cache", null, v, SQLiteDatabase.CONFLICT_REPLACE);
            prune();
        } catch (Exception ignored) {
        }
    }

    private void prune() {
        try {
            getWritableDatabase().delete("cache", "updated_at<?", new String[]{
                    String.valueOf(System.currentTimeMillis() - MAX_AGE)
            });
        } catch (Exception ignored) {
        }
    }
}
