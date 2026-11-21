package top.jzwall.pulse;

import android.app.job.JobInfo;
import android.app.job.JobParameters;
import android.app.job.JobScheduler;
import android.app.job.JobService;
import android.content.ComponentName;
import android.content.Context;

public class NotificationJobService extends JobService {
    private static final int JOB_ID = 0x4a5a;

    @Override
    public boolean onStartJob(final JobParameters params) {
        NotificationHelper.ensureChannel(this);
        new Thread(new Runnable() {
            @Override
            public void run() {
                try {
                    EventPoller.poll(getApplicationContext());
                } finally {
                    jobFinished(params, false);
                }
            }
        }, "jz-event-poll").start();
        return true;
    }

    @Override
    public boolean onStopJob(JobParameters params) {
        return false;
    }

    static void schedule(Context context) {
        try {
            JobScheduler scheduler = (JobScheduler) context.getSystemService(Context.JOB_SCHEDULER_SERVICE);
            if (scheduler == null) return;
            JobInfo job = new JobInfo.Builder(JOB_ID,
                    new ComponentName(context, NotificationJobService.class))
                    .setRequiredNetworkType(JobInfo.NETWORK_TYPE_ANY)
                    .setPeriodic(15 * 60 * 1000L)
                    .setPersisted(true)
                    .build();
            scheduler.schedule(job);
        } catch (Exception ignored) {
        }
    }
}
