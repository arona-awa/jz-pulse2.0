// 插件运行时：每插件一条串行消息队列。
//
// goja 的 Runtime 不是 goroutine-safe，所有脚本访问（setup、路由、事件、
// 过滤器、定时任务）都必须经过 Worker 串行化，插件作者因此无需考虑并发。
package plugin

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

var errWorkerClosed = errors.New("插件运行时已关闭")

type callReq struct {
	fn   func(*jsRuntime) (any, error)
	resp chan callResp
}

type callResp struct {
	val any
	err error
}

// Worker 串行执行某个插件的全部脚本调用
type Worker struct {
	rt     *jsRuntime
	jobs   chan *callReq
	quit   chan struct{}
	closed atomic.Bool
	once   sync.Once
}

// NewWorker 创建并启动 worker（rt 已装配好 Host API）
func NewWorker(rt *jsRuntime) *Worker {
	w := &Worker{
		rt:   rt,
		jobs: make(chan *callReq, 64),
		quit: make(chan struct{}),
	}
	go w.loop()
	return w
}

func (w *Worker) loop() {
	for {
		select {
		case req := <-w.jobs:
			w.exec(req)
		case <-w.quit:
			return
		}
	}
}

// exec 执行一次调用，保证异常不逃逸（panic 隔离）
func (w *Worker) exec(req *callReq) {
	defer func() {
		if r := recover(); r != nil {
			req.resp <- callResp{nil, fmt.Errorf("插件脚本异常: %v", r)}
		}
		// 清理可能的中断标记，确保后续调用可用
		defer func() { _ = recover() }()
		w.rt.vm.ClearInterrupt()
	}()
	val, err := req.fn(w.rt)
	req.resp <- callResp{val, err}
}

// Do 提交一次脚本调用；ctx 超时会中断虚拟机并返回错误
func (w *Worker) Do(ctx context.Context, fn func(*jsRuntime) (any, error)) (any, error) {
	if w == nil || w.rt == nil {
		return nil, errWorkerClosed
	}
	if w.closed.Load() {
		return nil, errWorkerClosed
	}
	req := &callReq{fn: fn, resp: make(chan callResp, 1)}
	select {
	case w.jobs <- req:
	case <-w.quit:
		return nil, errWorkerClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case r := <-req.resp:
		return r.val, r.err
	case <-ctx.Done():
		// 超时：中断正在执行的脚本，等 worker 回包后返回
		w.rt.Stop()
		select {
		case <-req.resp:
		case <-time.After(800 * time.Millisecond):
		}
		return nil, ctx.Err()
	}
}

// Closed 是否已关闭
func (w *Worker) Closed() bool { return w == nil || w.closed.Load() }

// Close 关闭 worker（幂等）
func (w *Worker) Close() {
	if w == nil {
		return
	}
	w.once.Do(func() {
		w.closed.Store(true)
		close(w.quit)
	})
}
