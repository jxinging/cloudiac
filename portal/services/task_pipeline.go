package services

import (
	"bytes"
	"cloudiac/common"
	"cloudiac/portal/consts/e"
	"cloudiac/portal/libs/db"
	"cloudiac/portal/models"
	"cloudiac/utils/logs"
	"path/filepath"

	"gopkg.in/yaml.v2"
)

func GenerateTaskPipeline(sess *db.Session, tplId models.Id, revision, workdir string) (pipeline models.Pipeline, er e.Error) {
	repo, er := GetVcsRepoByTplId(sess, tplId)
	if er != nil {
		return pipeline, er
	}

	paths := []string{filepath.Join(workdir, common.PipelineFileName)}
	if workdir != "" {
		paths = append(paths, common.PipelineFileName)
	}

	var (
		content []byte
		err     error
	)
	for _, path := range paths {
		content, err = repo.ReadFileContent(revision, path)
		// TODO 所有 vcs 的 ReadFileContent() 实现需要在文件不存在时返回 ObjectNotExists 错误
		if e.Is(err, e.ObjectNotExists) {
			continue
		}
		logs.Get().Debugf("read file content error(%T): %v", err, err)
		return pipeline, e.New(e.VcsError, err)
	}

	if len(content) == 0 {
		return models.DefaultPipeline(), nil
	}

	buffer := bytes.NewBuffer(content)
	if err := yaml.NewDecoder(buffer).Decode(&pipeline); err != nil {
		return pipeline, e.New(e.InvalidPipeline, err)
	}
	return pipeline, nil
}
