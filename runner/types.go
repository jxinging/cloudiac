// Copyright 2021 CloudJ Company Limited. All rights reserved.

package runner

/*
portal 和 runner 通信使用的消息结构体
*/

type EnvVariables struct {
}

type TaskEnv struct {
	Id           string `json:"id" binding:""`
	Workdir      string `json:"workdir"`
	TfVarsFile   string `json:"tfVarsFile"`
	Playbook     string `json:"playbook"`
	PlayVarsFile string `json:"playVarsFile"`
	TfVersion    string `json:"tfVersion"`

	EnvironmentVars map[string]string `json:"environment"`
	TerraformVars   map[string]string `json:"terraform"`
	AnsibleVars     map[string]string `json:"ansible"`
}

type StateStore struct {
	Backend string `json:"backend" binding:""`
	Scheme  string `json:"scheme" binding:""`
	Path    string `json:"path" binding:""`
	Address string `json:"address" binding:""` // consul 地址 runner 会自动设置
}

type RunTaskReq struct {
	Env          TaskEnv    `json:"env" binding:""`
	RunnerId     string     `json:"runnerId" binding:""`
	TaskId       string     `json:"taskId" binding:"required"`
	Step         int        `json:"step" binding:""`
	StepType     string     `json:"stepType" binding:"required"`
	StepArgs     []string   `json:"stepArgs"`
	DockerImage  string     `json:"dockerImage"`
	StateStore   StateStore `json:"stateStore" binding:""`
	RepoAddress  string     `json:"repoAddress" binding:""` // 带 token 的完整路径
	RepoBranch   string     `json:"repoBranch" binding:""`  // git branch or tag
	RepoCommitId string     `json:"repoCommitId" binding:""`

	SysEnvironments map[string]string `json:"sysEnvironments "` // 系统注入的环境变量

	Timeout    int    `json:"timeout"`
	PrivateKey string `json:"privateKey"`

	Policies        []TaskPolicy `json:"policies"` // 策略内容
	StopOnViolation bool         `json:"stopOnViolation"`

	Repos []Repository `json:"repos"` // 待扫描仓库列表

	ContainerId string `json:"containerId"`
	PauseTask   bool   `json:"pauseTask"` // 本次执行结束后暂停任务
}

type Repository struct {
	RepoAddress  string `json:"repoAddress" binding:""` // 带 token 的完整路径
	RepoRevision string `json:"repoRevision" binding:""`
}

type TaskStatusReq struct {
	EnvId  string `json:"envId" form:"envId" binding:""`
	TaskId string `json:"taskId" form:"taskId" binding:""`
	Step   int    `json:"step" form:"step" binding:""`
}

type TaskStopReq struct {
	TaskId       string   `json:"taskId" form:"taskId" binding:"required"`
	ContainerIds []string `json:"containerIds" form:"containerIds" binding:"required"`
}

type TaskPolicy struct {
	PolicyId string      `json:"policyId"`
	Meta     interface{} `json:"meta"`
	Rego     string      `json:"rego"`
}

type TaskLogReq TaskStatusReq

// TaskStatusMessage runner 通知任务状态到 portal
type TaskStatusMessage struct {
	Timeout bool `json:"timeout"` // 任务是否己超时？

	// 当 timeout 为 true 时，以下两个字段无意义
	Exited   bool `json:"exited"`
	ExitCode int  `json:"status_code"`

	LogContent           []byte `json:"logContent"`
	TfStateJson          []byte `json:"tfStateJson"`
	TfPlanJson           []byte `json:"tfPlanJson"`
	TfScanJson           []byte `json:"tfScanJson"`
	TfResultJson         []byte `json:"tfResultJson"`
	TFProviderSchemaJson []byte `json:"tfProviderSchemaJson"`
}

type ErrorMessage struct {
	Error string `json:"error"`
}

type Response struct {
	Error  string      `json:"error,omitempty"`
	Result interface{} `json:"result"`
}
