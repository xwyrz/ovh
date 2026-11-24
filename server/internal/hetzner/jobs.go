package hetzner

import (
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Job 一次后台异步任务（对照 hzc main.py 的 JOBS 机制）。
// 耗时操作（创建/重建/删除服务器）走后台 job，接口立刻返回 job id 供轮询。
type Job struct {
	ID        string      `json:"id"`
	Kind      string      `json:"kind"`
	Status    string      `json:"status"` // queued | running | success | error
	Result    interface{} `json:"result,omitempty"`
	Error     string      `json:"error,omitempty"`
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`
}

// JobRegistry 内存 job 注册表（进程内有效，重启即丢——与 hzc 一致）。
type JobRegistry struct {
	mu   sync.Mutex
	jobs map[string]*Job
}

// NewJobRegistry 构造。
func NewJobRegistry() *JobRegistry { return &JobRegistry{jobs: map[string]*Job{}} }

// Run 启动一个后台任务，返回 job id（12 位 hex，与 hzc 的 uuid 前 12 位同格式）。
func (r *JobRegistry) Run(kind string, fn func() (interface{}, error)) string {
	id := uuid.NewString()
	id = id[:12]
	job := &Job{ID: id, Kind: kind, Status: "queued", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	r.mu.Lock()
	r.jobs[id] = job
	r.mu.Unlock()
	go func() {
		r.mu.Lock()
		job.Status = "running"
		job.UpdatedAt = time.Now().UTC()
		r.mu.Unlock()
		res, err := fn()
		r.mu.Lock()
		defer r.mu.Unlock()
		job.UpdatedAt = time.Now().UTC()
		if err != nil {
			job.Status = "error"
			job.Error = err.Error()
			return
		}
		job.Status = "success"
		job.Result = res
	}()
	return id
}

// Get 取一个 job，不存在返回 nil。
func (r *JobRegistry) Get(id string) *Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	if !ok {
		return nil
	}
	cp := *j
	return &cp
}

// List 列出全部 job（最新的在前，最多 100 条）。
func (r *JobRegistry) List() []*Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Job, 0, len(r.jobs))
	for _, j := range r.jobs {
		cp := *j
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].CreatedAt.After(out[k].CreatedAt) })
	if len(out) > 100 {
		out = out[:100]
	}
	return out
}
