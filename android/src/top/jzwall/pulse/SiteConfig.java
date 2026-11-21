package top.jzwall.pulse;

final class SiteConfig {
    static final String BASE_URL = "https://jz-wall.top";
    static final String HOST = "jz-wall.top";
    static final String WWW_HOST = "www.jz-wall.top";
    static final String EVENTS_URL = BASE_URL + "/api/v1/events";

    static boolean isSiteHost(String host) {
        return HOST.equalsIgnoreCase(host) || WWW_HOST.equalsIgnoreCase(host);
    }

    static String postUrl(long postId) {
        return BASE_URL + "/post/" + postId;
    }

    private SiteConfig() {
    }
}
