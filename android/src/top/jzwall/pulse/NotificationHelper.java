package top.jzwall.pulse;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.content.Context;
import android.content.Intent;
import android.os.Build;

import org.json.JSONObject;

final class NotificationHelper {
    private static final String CHANNEL_ID = "jz_events";

    static void ensureChannel(Context context) {
        if (Build.VERSION.SDK_INT < 26) return;
        NotificationManager nm = (NotificationManager) context.getSystemService(Context.NOTIFICATION_SERVICE);
        if (nm == null) return;
        NotificationChannel channel = new NotificationChannel(
                CHANNEL_ID, "校园墙动态", NotificationManager.IMPORTANCE_HIGH);
        channel.setDescription("新帖、点赞、评论和投币通知");
        channel.enableVibration(true);
        channel.setShowBadge(true);
        nm.createNotificationChannel(channel);
    }

    static void showEvent(Context context, JSONObject event) {
        try {
            NotificationManager nm = (NotificationManager) context.getSystemService(Context.NOTIFICATION_SERVICE);
            if (nm == null || !nm.areNotificationsEnabled()) return;

            long eventId = event.optLong("id", 0);
            long postId = event.optLong("post_id", 0);
            long refId = event.optLong("ref_id", 0);
            String type = event.optString("type", "");
            String actor = event.optString("actor", "访客");
            if (actor == null || actor.trim().isEmpty()) actor = "访客";
            String excerpt = event.optString("excerpt", "").trim();

            String title;
            String text;
            if ("post".equals(type)) {
                title = "校园墙有新帖子";
                text = actor + " 发布了新帖" + (excerpt.isEmpty() ? "" : "：" + excerpt);
            } else if ("like".equals(type)) {
                title = "你的帖子被点赞";
                text = actor + " 赞了你的帖子" + (excerpt.isEmpty() ? "" : "：" + excerpt);
            } else if ("comment".equals(type)) {
                title = "你的帖子有新评论";
                text = actor + " 评论了你的帖子" + (excerpt.isEmpty() ? "" : "：" + excerpt);
            } else if ("tip".equals(type)) {
                title = "你的帖子收到投币";
                text = actor + " 投了 " + Math.max(1, refId) + " 枚硬币" +
                        (excerpt.isEmpty() ? "" : "：" + excerpt);
            } else {
                return;
            }

            Intent intent = new Intent(context, MainActivity.class);
            intent.setFlags(Intent.FLAG_ACTIVITY_NEW_TASK | Intent.FLAG_ACTIVITY_CLEAR_TOP |
                    Intent.FLAG_ACTIVITY_SINGLE_TOP);
            if (postId > 0) intent.putExtra("open_url", SiteConfig.postUrl(postId));
            int requestCode = (int) (eventId % 1000000000L);
            PendingIntent pi = PendingIntent.getActivity(context, requestCode, intent,
                    PendingIntent.FLAG_UPDATE_CURRENT | PendingIntent.FLAG_IMMUTABLE);

            Notification.Builder builder = Build.VERSION.SDK_INT >= 26
                    ? new Notification.Builder(context, CHANNEL_ID)
                    : new Notification.Builder(context);
            builder.setSmallIcon(R.drawable.ic_notify)
                    .setContentTitle(title)
                    .setContentText(text)
                    .setStyle(new Notification.BigTextStyle().bigText(text))
                    .setAutoCancel(true)
                    .setOnlyAlertOnce(false)
                    .setContentIntent(pi)
                    .setCategory(Notification.CATEGORY_SOCIAL)
                    .setVisibility(Notification.VISIBILITY_PRIVATE)
                    .setColor(0xFFE5484D)
                    .setWhen(System.currentTimeMillis())
                    .setShowWhen(true);

            int notificationId = (int) (eventId % 1000000000L);
            if (notificationId == 0) notificationId = 1;
            nm.notify(notificationId, builder.build());
        } catch (Exception ignored) {
        }
    }

    private NotificationHelper() {
    }
}
