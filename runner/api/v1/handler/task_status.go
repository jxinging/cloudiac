// Copyright 2021 CloudJ Company Limited. All rights reserved.

package handler

import (
	"cloudiac/runner"
	"cloudiac/runner/api/ctx"
	"cloudiac/runner/ws"
	"cloudiac/utils"
	"cloudiac/utils/logs"
	"context"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

var logger = logs.Get()

func TaskStatus(c *ctx.Context) {
	req := runner.TaskStatusReq{}
	if err := c.BindQuery(&req); err != nil {
		c.Error(err, http.StatusBadRequest)
		return
	}

	task, err := runner.LoadStartedTask(req.EnvId, req.TaskId, req.Step)
	if err != nil {
		if os.IsNotExist(err) {
			c.Error(err, http.StatusNotFound)
		} else {
			c.Error(err, http.StatusInternalServerError)
		}
		return
	}

	logger := logger.WithField("taskId", task.TaskId)
	wsConn, peerClosed, err := ws.UpgradeWithNotifyClosed(c.Writer, c.Request, nil)
	if err != nil {
		logger.Warnln(err)
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	defer func() {
		wsConn.Close()
	}()

	if err := doTaskStatus(wsConn, task, peerClosed); err != nil {
		logger.Errorln(err)
		_ = utils.WebsocketCloseWithCode(wsConn, websocket.CloseInternalServerErr, err.Error())
	} else {
		_ = utils.WebsocketClose(wsConn)
	}
}

func doTaskStatus(wsConn *websocket.Conn, task *runner.StartedTask, closedCh <-chan struct{}) error {
	logger := logger.WithField("taskId", task.TaskId).WithField("step", task.Step)

	// 获取任务最新状态并通过 websocket 发送
	sendStatus := func(withLog bool, isDeadline bool) error {
		var msg runner.TaskStatusMessage
		if isDeadline {
			msg.Timeout = true
		} else {
			status, err := task.Status()
			if err != nil {
				return err
			}
			msg = runner.TaskStatusMessage{
				Exited:   !status.Running,
				ExitCode: status.ExitCode,
			}
		}

		// 由于任务退出的时候 portal 会断开连接，所以如果判断已经退出，则直接发送全量日志
		if withLog || msg.Timeout || msg.Exited {
			logContent, err := runner.FetchTaskLog(task.EnvId, task.TaskId, task.Step)
			if err != nil {
				logger.Errorf("fetch task log error: %v", err)
				msg.LogContent = utils.TaskLogMsgBytes("Fetch task log error: %v", err)
			} else {
				msg.LogContent = logContent
			}

			if stateJson, err := runner.FetchStateJson(task.EnvId, task.TaskId); err != nil {
				logger.Errorf("fetch terraform state json error: %v", err)
			} else {
				msg.TfStateJson = stateJson
			}

			if providerJson, err := runner.FetchProviderJson(task.EnvId, task.TaskId); err != nil {
				logger.Errorf("fetch terraform provider json error: %v", err)
			} else {
				msg.TFProviderSchemaJson = providerJson
			}

			if planJson, err := runner.FetchPlanJson(task.EnvId, task.TaskId); err != nil {
				logger.Errorf("fetch terraform state json error: %v", err)
			} else {
				msg.TfPlanJson = planJson
			}

			if parseJson, err := runner.FetchJson(task.EnvId, task.TaskId, runner.TerrascanJsonFile); err != nil {
				logger.Errorf("fetch terrascan parsed json error: %v", err)
			} else {
				msg.TfScanJson = parseJson
			}
			if resultJson, err := runner.FetchJson(task.EnvId, task.TaskId, runner.TerrascanResultFile); err != nil {
				logger.Errorf("fetch terrascan scan result json error: %v", err)
			} else {
				msg.TfResultJson = resultJson
			}
		}

		if err := wsConn.WriteJSON(msg); err != nil {
			logger.Warnf("write message error: %v", err)
			return err
		}
		return nil
	}

	ctx, cancelFun := context.WithCancel(context.Background())
	defer cancelFun()

	waitCh := make(chan error, 1)
	go func() {
		defer close(waitCh)

		_, err := task.Wait(ctx)
		waitCh <- err
	}()

	// 发送首次结果
	if err := sendStatus(false, false); err != nil {
		return err
	}

	ticker := time.NewTicker(time.Second * 30)
	defer ticker.Stop()

	logger.Infof("watching task status")
	defer logger.Infof("watch task status done")

	closed := false
	for {
		if !closed {
			select {
			case <-closedCh:
				// 对端断开连接，我们只是通知 ctx canceled，然后继续等待 Wait 协程退出
				logger.Debugf("connection closed")
				cancelFun()
				closed = true
			default:
			}
		}

		select {
		case <-ticker.C:
			// 定时发送最新任务状态
			if err := sendStatus(false, false); err != nil {
				logger.Warnf("send status error: %v", err)
			}
		case err := <-waitCh:
			if closed { // 对端己断开连接
				return nil
			}
			if err != nil {
				if err == context.DeadlineExceeded {
					return sendStatus(true, true)
				}
				return err
			}
			// 任务结束，发送状态和全量日志
			return sendStatus(true, false)
		}
	}
}
