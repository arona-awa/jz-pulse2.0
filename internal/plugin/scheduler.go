// 应用型插件定时任务调度。
//
// 每个任务一条 goroutine + Ticker，加载时启动、卸载时停止；
// 触发走沙箱（超时、权限、审计），失败计入熔断。
package plugin

import (
	"context"
	"sync"
	"time"

	"clearlove/internal/util"
)

var (
	jobMu    sync.Mutex
	jobStops = map[string]chan struct{}{} // dir -> stop
)

// startJobs 为插件的全部任务启动调度
func startJobs(a *App) {
	if a == nil || len(a.Jobs) == 0 {
		return
	}
	stopJobs(a.Dir)
	stop := make(chan struct{})
	jobMu.Lock()
	jobStops[a.Dir] = stop
	jobMu.Unlock()

	for _, j := range a.Jobs {
		j := j
		go func() {
			ticker := time.NewTicker(j.Every)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					runJob(a, j)
				}
			}
		}()
	}
	util.Log("info", "插件 %s 已注册 %d 个定时任务", a.Plugin.SlugOf(), len(a.Jobs))
}

// stopJobs 停止插件的全部任务
func stopJobs(dir string) {
	jobMu.Lock()
	stop, ok := jobStops[dir]
	if ok {
		delete(jobStops, dir)
	}
	jobMu.Unlock()
	if ok {
		close(stop)
	}
}

func runJob(a *App, j JobReg) {
	if a.Worker == nil || a.Worker.Closed() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), a.Plugin.Timeout())
	defer cancel()
	start := time.Now()
	_, err := a.Worker.Do(ctx, func(rt *jsRuntime) (any, error) {
		rt.cur = nil
		_, err := rt.callFn(j.Fn)
		return nil, err
	})
	if err != nil {
		noteFailure(a, err)
		logPlugin(a.Plugin.SlugOf(), "job:"+j.Name, "执行失败: "+err.Error(), time.Since(start))
		return
	}
	noteSuccess(a)
	logPlugin(a.Plugin.SlugOf(), "job:"+j.Name, "执行完成", time.Since(start))
}

// JobCount 当前注册的任务数（后台展示用）
func JobCount() int {
	jobMu.Lock()
	defer jobMu.Unlock()
	return len(jobStops)
}
