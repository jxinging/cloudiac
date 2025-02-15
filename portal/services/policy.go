// Copyright 2021 CloudJ Company Limited. All rights reserved.

package services

import (
	"cloudiac/common"
	"cloudiac/portal/consts"
	"cloudiac/portal/consts/e"
	"cloudiac/portal/libs/db"
	"cloudiac/portal/models"
	"cloudiac/portal/models/forms"
	"cloudiac/runner"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func CreatePolicy(tx *db.Session, policy *models.Policy) (*models.Policy, e.Error) {
	if err := models.Create(tx, policy); err != nil {
		if e.IsDuplicate(err) {
			return nil, e.New(e.PolicyAlreadyExist, err)
		}
		return nil, e.AutoNew(err, e.DBError)
	}
	return policy, nil
}

// GetPolicyReferenceId 生成策略ID
// reference id = "iac" + policy type + creator scope + max id
func GetPolicyReferenceId(query *db.Session, policy *models.Policy) (string, e.Error) {
	typ := "iac"
	if policy.PolicyType != "" {
		typ = policy.PolicyType
	}
	lastId := 0
	// query max id by type
	po := models.Policy{}
	if err := query.Model(models.Policy{}).Where("reference_id LIKE ?", "iac_"+typ+"%").
		Order("length(reference_id) DESC, reference_id DESC").Last(&po); err != nil && !e.IsRecordNotFound(err) {
		return "", e.AutoNew(err, e.DBError)
	}
	idx := strings.LastIndex(po.ReferenceId, "_")
	if idx != -1 {
		lastId, _ = strconv.Atoi(po.ReferenceId[idx+1:])
	}

	// internal or public
	scope := "public"

	return fmt.Sprintf("%s_%s_%s_%d", "iac", typ, scope, lastId+1), nil
}

func GetPolicyById(tx *db.Session, id models.Id) (*models.Policy, e.Error) {
	po := models.Policy{}
	if err := tx.Model(models.Policy{}).Where("id = ?", id).First(&po); err != nil {
		if e.IsRecordNotFound(err) {
			return nil, e.New(e.PolicyNotExist, err)
		}
		return nil, e.New(e.DBError, err)
	}
	return &po, nil
}

func GetTaskPolicies(query *db.Session, taskId models.Id) ([]runner.TaskPolicy, e.Error) {
	var taskPolicies []runner.TaskPolicy

	task, err := GetScanTaskById(query, taskId)
	if err != nil {
		return nil, err
	}

	policies, _, err := GetValidPolicies(query, task.TplId, task.EnvId)
	if err != nil {
		return nil, err
	}

	for _, policy := range policies {
		category := "general"
		group, _ := GetPolicyGroupById(query, policy.GroupId)
		if group != nil {
			category = group.Name
		}
		meta := map[string]interface{}{
			"name":          policy.RuleName,
			"file":          "policy.rego",
			"policy_type":   policy.PolicyType,
			"resource_type": policy.ResourceType,
			"severity":      strings.ToUpper(policy.Severity),
			"reference_id":  policy.ReferenceId,
			"category":      category,
			"version":       policy.Revision,
			"id":            string(policy.Id),
		}
		taskPolicies = append(taskPolicies, runner.TaskPolicy{
			PolicyId: string(policy.Id),
			Meta:     meta,
			Rego:     policy.Rego,
		})
	}
	return taskPolicies, nil
}

// GetValidPolicies 获取云模板/环境关联的策略
func GetValidPolicies(query *db.Session, tplId, envId models.Id) (validPolicies []models.Policy, suppressedPolicies []models.Policy, err e.Error) {
	var (
		tplPolicies, tplValidPolicies, tplSuppressedPolicies []models.Policy
		envPolicies, envSuppressedPolicies                   []models.Policy
		enabled                                              bool
	)

	// 获取云模板策略
	if enabled, err = IsTemplateEnabledScan(query, tplId); err != nil {
		return
	}
	if enabled {
		if tplPolicies, err = GetPoliciesByTemplateId(query, tplId); err != nil {
			return
		}
		if tplValidPolicies, tplSuppressedPolicies, err = FilterSuppressPolicies(query, tplPolicies, tplId, consts.ScopeTemplate); err != nil {
			return
		}
	}

	// 获取环境策略
	if enabled, err = IsEnvEnabledScan(query, envId); err != nil {
		return
	}
	if envId != "" && enabled {
		if envPolicies, err = GetPoliciesByEnvId(query, envId); err != nil {
			return
		}

		// 有效策略：valid = tpl valid + env valid - env suppress
		policies := MergePolicies(tplValidPolicies, envPolicies)
		if validPolicies, envSuppressedPolicies, err = FilterSuppressPolicies(query, policies, envId, consts.ScopeEnv); err != nil {
			return
		}

		// 屏蔽策略列表需要排除掉环境引入的策略
		// suppress = tpl suppress - env valid + env suppress
		envValidPoliciesMap := make(map[models.Id]int)
		for _, policy := range envPolicies {
			envValidPoliciesMap[policy.Id] = 1
		}
		for idx, policy := range tplSuppressedPolicies {
			if _, ok := envValidPoliciesMap[policy.Id]; !ok {
				suppressedPolicies = append(suppressedPolicies, tplSuppressedPolicies[idx])
			}
		}
		suppressedPolicies = MergePolicies(suppressedPolicies, envSuppressedPolicies)
	} else {
		validPolicies = tplValidPolicies
		suppressedPolicies = tplSuppressedPolicies
	}

	return
}

func suppressedQuery(query *db.Session, envId models.Id, tplId models.Id) *db.Session {
	q := query.Select("iac_policy.id").Table(models.PolicyRel{}.TableName()).
		Joins("join iac_policy on iac_policy.group_id = iac_policy_rel.group_id").
		Where("iac_policy.enabled = 1").
		Joins("join iac_policy_group on iac_policy_group.id = iac_policy_rel.group_id").
		Where("iac_policy_group.enabled = 1")

	if envId != "" {
		q = q.Where("iac_policy_rel.env_id = ?", envId)
	} else if tplId != "" {
		q = q.Where("iac_policy_rel.tpl_id = ?", tplId)
	}

	suppressQuery := query.Model(models.PolicySuppress{}).Select("policy_id")
	if envId != "" {
		suppressQuery = suppressQuery.Where("target_type = 'env' AND env_id = ?", envId)
		q = q.Where("iac_policy.id not in (?)", suppressQuery.Expr())
	} else if tplId != "" {
		suppressQuery = suppressQuery.Where("target_type = 'template' AND tpl_id = ?", tplId)
		q = q.Where("iac_policy.id not in (?)", suppressQuery.Expr())
	}

	enableQuery := query.Model(models.PolicyRel{}).Where("iac_policy_rel.group_id = '' and iac_policy_rel.enabled = 1")
	if envId != "" {
		enableQuery = enableQuery.Select("env_id").Where("env_id = ?", envId)
		q = q.Where("iac_policy_rel.env_id in (?)", enableQuery.Expr())
	} else if tplId != "" {
		enableQuery = enableQuery.Select("tpl_id").Where("tpl_id = ?", tplId)
		q = q.Where("iac_policy_rel.tpl_id in (?)", enableQuery.Expr())
	}

	return q
}

// GetPoliciesByEnvId 查询环境关联的所有策略
func GetPoliciesByEnvId(query *db.Session, envId models.Id) ([]models.Policy, e.Error) {
	var policies []models.Policy
	q := query.Model(models.Policy{}).
		Joins("join iac_policy_group on iac_policy_group.id = iac_policy.group_id").
		Joins("join iac_policy_rel on iac_policy_rel.group_id = iac_policy_group.id").
		Where("iac_policy_rel.env_id = ? and iac_policy_rel.scope = ?", envId, consts.ScopeEnv)
	if err := q.Find(&policies); err != nil {
		if e.IsRecordNotFound(err) {
			return nil, e.New(e.PolicyNotExist, err)
		}
		return nil, e.New(e.DBError, err)
	}

	return policies, nil
}

// GetPoliciesByTemplateId 查询云模板关联的所有策略
func GetPoliciesByTemplateId(query *db.Session, tplId models.Id) ([]models.Policy, e.Error) {
	var policies []models.Policy
	q := query.Model(models.Policy{}).
		Joins("join iac_policy_group on iac_policy_group.id = iac_policy.group_id").
		Joins("join iac_policy_rel on iac_policy_rel.group_id = iac_policy_group.id").
		Where("iac_policy_rel.tpl_id = ? and iac_policy_rel.scope = ?", tplId, consts.ScopeTemplate)
	if err := q.Find(&policies); err != nil {
		if e.IsRecordNotFound(err) {
			return nil, e.New(e.PolicyNotExist, err)
		}
		return nil, e.New(e.DBError, err)
	}

	return policies, nil
}

func UpdatePolicy(tx *db.Session, policy *models.Policy, attr models.Attrs) (int64, e.Error) {
	affected, err := models.UpdateAttr(tx, policy, attr)
	if err != nil {
		if e.IsDuplicate(err) {
			return affected, e.New(e.PolicyGroupAlreadyExist, err)
		}
		return affected, e.AutoNew(err, e.DBError)
	}
	return affected, nil
}

//RemovePoliciesGroupRelation 移除策略组和策略的关系
func RemovePoliciesGroupRelation(tx *db.Session, groupId models.Id) e.Error {
	if _, err := UpdatePolicy(tx.Where("group_id = ?", groupId),
		&models.Policy{}, models.Attrs{"group_id": ""}); err != nil {
		return err
	}
	return nil
}

func SearchPolicy(dbSess *db.Session, form *forms.SearchPolicyForm) *db.Session {
	pTable := models.Policy{}.TableName()
	query := dbSess.Table(pTable)
	if len(form.GroupId) > 0 {
		query = query.Where(fmt.Sprintf("%s.group_id in (?)", pTable), form.GroupId)
	}

	if form.Severity != "" {
		query = query.Where(fmt.Sprintf("%s.severity = ?", pTable), form.Severity)
	}

	if form.Q != "" {
		qs := "%" + form.Q + "%"
		query = query.Where(fmt.Sprintf("%s.name like ?", pTable), qs)
	}

	query = query.Joins("left join iac_policy_group as g on g.id = iac_policy.group_id").
		LazySelectAppend("iac_policy.*,g.name as group_name")

	query = query.Joins("left join iac_user as u on u.id = iac_policy.creator_id").
		LazySelectAppend("iac_policy.*,u.name as creator")

	return query
}

func DeletePolicy(dbSess *db.Session, id models.Id) (interface{}, e.Error) {
	if _, err := dbSess.
		Where("id = ?", id).
		Delete(&models.Policy{}); err != nil {
		return nil, e.New(e.DBError, err)
	}

	return nil, nil
}

func DetailPolicy(dbSess *db.Session, id models.Id) (interface{}, e.Error) {
	p := models.Policy{}
	if err := dbSess.Table(models.Policy{}.TableName()).
		Where("id = ?", id).
		First(&p); err != nil {
		if e.IsRecordNotFound(err) {
			return nil, e.New(e.DBError, fmt.Errorf("polict not found id: %s", id))
		}
		return nil, e.New(e.DBError, err)
	}
	return p, nil
}

func SearchPolicyTpl(dbSess *db.Session, orgId, tplId models.Id, q string) *db.Session {
	query := dbSess.Table("iac_template AS tpl").Where("tpl.deleted_at_t = 0")
	if orgId != "" {
		query = query.Where("tpl.org_id = ?", orgId)
	}
	if tplId != "" {
		query = query.Where("tpl.id = ?", tplId)
	}
	if q != "" {
		query = query.WhereLike("tpl.name", q)
	}
	query = query.Joins("LEFT JOIN iac_scan_task AS task ON task.id = tpl.last_scan_task_id")
	return query.LazySelect("tpl.*, task.policy_status").
		Joins("LEFT JOIN iac_policy_rel on iac_policy_rel.tpl_id = tpl.id and iac_policy_rel.group_id = ''").
		Joins("LEFT JOIN iac_org on iac_org.id = tpl.org_id").
		LazySelectAppend("iac_policy_rel.enabled", "iac_org.name as org_name").
		Order("iac_org.created_at desc, tpl.created_at desc ")
}

func SearchPolicyEnv(dbSess *db.Session, orgId, projectId, envId models.Id, q string) *db.Session {
	envTable := models.Env{}.TableName()
	query := dbSess.Table(envTable).Where(fmt.Sprintf("%s.archived = 0", envTable))
	if orgId != "" {
		query = query.Where(fmt.Sprintf("%s.org_id = ?", envTable), orgId)
	}
	if projectId != "" {
		query = query.Where(fmt.Sprintf("%s.project_id = ?", envTable), projectId)
	}
	if envId != "" {
		query = query.Where(fmt.Sprintf("%s.id = ?", envTable), envId)
	}

	if q != "" {
		query = query.WhereLike(fmt.Sprintf("%s.name", envTable), q)
	}

	query = query.Joins(fmt.Sprintf("LEFT JOIN %s AS tpl ON tpl.id = %s.tpl_id",
		models.Template{}.TableName(), envTable))
	query = query.Joins(fmt.Sprintf("LEFT JOIN %s AS task ON task.id = %s.last_scan_task_id",
		models.ScanTask{}.TableName(), envTable))

	return query.
		LazySelectAppend(fmt.Sprintf("%s.*", envTable)).
		LazySelectAppend("tpl.name AS template_name, tpl.id AS tpl_id, tpl.repo_addr AS repo_addr").
		LazySelectAppend("task.policy_status").
		Joins("LEFT JOIN iac_policy_rel on iac_policy_rel.env_id = iac_env.id and iac_policy_rel.group_id = ''").
		Joins("LEFT JOIN iac_org as org on org.id = iac_env.org_id").
		Joins("LEFT JOIN iac_project as project on project.id = iac_env.project_id").
		LazySelectAppend("iac_policy_rel.enabled", "org.name as org_name, project.name as project_name").
		Order("org.created_at desc, project.created_at desc, iac_env.created_at desc")
}

func EnvOfPolicy(dbSess *db.Session, form *forms.EnvOfPolicyForm, orgId, projectId models.Id) *db.Session {
	pTable := models.Policy{}.TableName()
	query := dbSess.Table(pTable).Joins(fmt.Sprintf("left join %s as pg on pg.id = %s.group_id",
		models.PolicyGroup{}.TableName(), pTable)).LazySelectAppend("pg.name as group_name, pg.id as group_id")
	if form.GroupId != "" {
		query = query.Where(fmt.Sprintf("%s.group_id = ?", pTable), form.GroupId)
	}

	if form.Severity != "" {
		query = query.Where(fmt.Sprintf("%s.severity = ?", pTable), form.Severity)
	}

	if form.Q != "" {
		query = query.WhereLike(fmt.Sprintf("%s.name", pTable), form.Q)
	}

	query = query.
		Joins(fmt.Sprintf("left join %s as rel on rel.group_id = pg.id ", models.PolicyRel{}.TableName())).
		Joins(fmt.Sprintf("left join %s as env on env.id = rel.env_id", models.Env{}.TableName())).
		Where("rel.org_id = ? and rel.project_id = ? and rel.scope = ?", orgId, projectId, models.PolicyRelScopeEnv)

	return query.LazySelectAppend(fmt.Sprintf("env.name as env_name, %s.*", pTable))
}

func TplOfPolicy(dbSess *db.Session, form *forms.TplOfPolicyForm, orgId, projectId models.Id) *db.Session {
	pTable := models.Policy{}.TableName()
	query := dbSess.Table(pTable).Joins(fmt.Sprintf("left join %s as pg on pg.id = %s.group_id",
		models.PolicyGroup{}.TableName(), pTable)).LazySelectAppend("pg.name as group_name, pg.id as group_id")
	if form.GroupId != "" {
		query = query.Where(fmt.Sprintf("%s.group_id = ?", pTable), form.GroupId)
	}

	if form.Severity != "" {
		query = query.Where(fmt.Sprintf("%s.severity = ?", pTable), form.Severity)
	}

	if form.Q != "" {
		query = query.WhereLike(fmt.Sprintf("%s.name", pTable), form.Q)
	}

	query = query.
		Joins(fmt.Sprintf("left join %s as rel on rel.group_id = pg.id ", models.PolicyRel{}.TableName())).
		Joins(fmt.Sprintf("left join %s as tpl on tpl.id = rel.tpl_id", models.Template{}.TableName())).
		Where("rel.org_id = ? and rel.project_id = ? and rel.scope = ?", orgId, projectId, models.PolicyRelScopeTpl)

	return query.LazySelectAppend(fmt.Sprintf("tpl.name as tpl_name, %s.*", pTable))
}

func TplOfPolicyGroup(dbSess *db.Session, form *forms.TplOfPolicyGroupForm) *db.Session {
	pTable := models.PolicyGroup{}.TableName()
	query := dbSess.Table(pTable)
	query = query.
		Joins(fmt.Sprintf("left join %s as rel on rel.group_id = iac_policy_group.id and rel.tpl_id = ?", models.PolicyRel{}.TableName()), form.Id).
		Where("rel.scope = ?", models.PolicyRelScopeTpl)

	return query.LazySelectAppend(fmt.Sprintf("%s.id as group_id, %s.name as group_name", pTable, pTable)).Order("group_name desc")
}

func PolicyError(query *db.Session, policyId models.Id) *db.Session {
	lastScanQuery := query.Model(models.PolicyResult{}).
		Select("max(id)").
		Group("env_id,tpl_id")
	lastTaskQuery := query.Model(models.PolicyResult{}).
		Select("task_id").
		Where("id in (?)", lastScanQuery.Expr())

	return query.Model(models.PolicyResult{}).
		Select(fmt.Sprintf("if(%s.env_id='','template','env')as target_id,%s.*,%s.name as env_name,%s.name as template_name",
			models.PolicyResult{}.TableName(),
			models.PolicyResult{}.TableName(),
			models.Env{}.TableName(),
			models.Template{}.TableName(),
		)).
		Joins("LEFT JOIN iac_env ON iac_policy_result.env_id = iac_env.id").
		Joins("LEFT JOIN iac_template ON iac_policy_result.tpl_id = iac_template.id").
		Where("iac_policy_result.policy_id = ?", policyId).
		Where("iac_policy_result.status = ? OR iac_policy_result.status = ?",
			common.PolicyStatusFailed, common.PolicyStatusViolated).
		Where("iac_policy_result.task_id in (?)", lastTaskQuery.Expr())
}

type PolicyScanSummary struct {
	Id     models.Id `json:"id"`
	Count  int       `json:"count"`
	Status string    `json:"status"`
}

// PolicySummary 获取策略/策略组/任务执行结果
func PolicySummary(query *db.Session, ids []models.Id, scope string) ([]*PolicyScanSummary, e.Error) {
	var key string
	switch scope {
	case consts.ScopePolicy:
		key = "policy_id"
	case consts.ScopePolicyGroup:
		key = "policy_group_id"
	case consts.ScopeTask:
		key = "task_id"
	}
	subQuery := query.Model(models.PolicyResult{}).Select("max(id)").Where(fmt.Sprintf("%s in (?)", key), ids)

	if scope == consts.ScopeTask {
		subQuery = subQuery.Group(fmt.Sprintf("%s,env_id,tpl_id,policy_id", key))
	} else {
		subQuery = subQuery.Group(fmt.Sprintf("%s,env_id,tpl_id", key))
	}

	q := query.Model(models.PolicyResult{}).Select(fmt.Sprintf("%s as id,count(*) as count,status", key)).
		Where("id in (?)", subQuery.Expr()).Group(fmt.Sprintf("%s,status", key))

	summary := make([]*PolicyScanSummary, 0)
	if err := q.Find(&summary); err != nil {
		if e.IsRecordNotFound(err) {
			return nil, nil
		}
		return nil, e.New(e.DBError, err)
	}

	return summary, nil
}

type ScanStatus struct {
	Date   string
	Count  int
	Status string
}

func GetPolicyScanStatus(query *db.Session, id models.Id, from time.Time, to time.Time, scope string) ([]*ScanStatus, e.Error) {
	key := ""
	switch scope {
	case consts.ScopePolicyGroup:
		key = "policy_group_id"
	case consts.ScopePolicy:
		key = "policy_id"
	}
	q := query.Model(models.PolicyResult{})
	q = q.Where(fmt.Sprintf("start_at >= ? and start_at < ? and %s = ?", key), from, to, id).
		Select("count(*) as count, date(start_at) as date, status").
		Group("date(start_at), status").
		Order("date(start_at)")

	scanStatus := make([]*ScanStatus, 0)
	if err := q.Find(&scanStatus); err != nil {
		if e.IsRecordNotFound(err) {
			return scanStatus, nil
		}
		return nil, e.New(e.DBError, err)
	}

	return scanStatus, nil
}

type ScanStatusByTarget struct {
	ID     string
	Count  int
	Status string
	Name   string
}

func GetPolicyScanByTarget(query *db.Session, policyId models.Id, from, to time.Time, showCount int) ([]*ScanStatusByTarget, e.Error) {
	groupQuery := query.Model(models.PolicyResult{})
	groupQuery = groupQuery.Where("start_at >= ? and start_at < ? and policy_id = ?", from, to, policyId).
		Where("status != 'pending'"). // 跳过pending状态
		Select("count(*) as count, tpl_id, env_id").
		Group("tpl_id,env_id")

	q := query.Table("(?) as r", groupQuery.Expr()).
		Select("r.*,if(r.env_id = '', iac_template.name, iac_env.name) as name").
		Joins("left join iac_env on iac_env.id = r.env_id").
		Joins("left join iac_template on iac_template.id = r.tpl_id")
	q = q.Order("count desc").Limit(showCount)
	scanStatus := make([]*ScanStatusByTarget, 0)
	if err := q.Find(&scanStatus); err != nil {
		if e.IsRecordNotFound(err) {
			return scanStatus, nil
		}
		return nil, e.New(e.DBError, err)
	}

	return scanStatus, nil
}

func SearchGroupOfPolicy(dbSess *db.Session, groupId models.Id, bind bool) *db.Session {
	query := dbSess.Table(models.Policy{}.TableName())
	if bind {
		query = query.Where("group_id = ? ", groupId)
	} else {
		query = query.Where("group_id = '' or group_id is null")
	}
	return query
}

// PolicyTargetSummary 获取策略环境/云模板执行结果
func PolicyTargetSummary(query *db.Session, ids []models.Id, scope string) ([]*PolicyScanSummary, e.Error) {
	var (
		key   string
		table string
	)
	switch scope {
	case consts.ScopeEnv:
		key = "env_id"
		table = "iac_env"
	case consts.ScopeTemplate:
		key = "tpl_id"
		table = "iac_template"
	}
	q := query.Model(models.PolicyResult{}).
		Select(fmt.Sprintf("iac_policy_result.%s as id,count(*) as count,iac_policy_result.status", key)).
		Joins(fmt.Sprintf("join %s on %s.id = iac_policy_result.%s and %s.last_scan_task_id = iac_policy_result.task_id",
			table, table, key, table)).
		Where(fmt.Sprintf("iac_policy_result.%s in (?)", key), ids).
		Group(fmt.Sprintf("%s,status", key))

	summary := make([]*PolicyScanSummary, 0)
	if err := q.Find(&summary); err != nil {
		if e.IsRecordNotFound(err) {
			return nil, nil
		}
		return nil, e.New(e.DBError, err)
	}

	return summary, nil
}

func SubQueryUserEnvIds(query *db.Session, userId models.Id) *db.Session {
	q := query.Model(models.Env{}).Select("id")

	// 系统管理员
	if UserIsSuperAdmin(query, userId) {
		return q
	}

	// 组织管理员相关项目
	var orgAdminIds []models.Id
	userOrgs := getUserOrgs(userId)
	orgAdminProjectQuery := query.Model(models.Project{}).Select("id")
	for _, userOrg := range userOrgs {
		if UserHasOrgRole(userId, userOrg.OrgId, consts.OrgRoleAdmin) {
			orgAdminIds = append(orgAdminIds, userOrg.OrgId)
		}
	}

	// 普通成员相关项目
	var projectIds []models.Id
	userProjects := getUserProjects(userId)
	for _, userProject := range userProjects {
		projectIds = append(projectIds, userProject.ProjectId)
	}

	if len(orgAdminIds) > 0 && len(projectIds) > 0 {
		return q.Where("org_id in (?) OR project_id in (?)", orgAdminProjectQuery, projectIds)
	} else if len(orgAdminIds) > 0 {
		return q.Where("org_id in (?)", orgAdminProjectQuery)
	} else if len(projectIds) > 0 {
		return q.Where("project_id in (?)", projectIds)
	} else {
		return q.Where("1 = 0")
	}
}

func SubQueryUserTemplateIds(query *db.Session, userId models.Id) *db.Session {
	q := query.Model(models.Template{}).Select("id")

	// 平台管理员
	if UserIsSuperAdmin(query, userId) {
		return q
	}

	// 组织内用户
	userOrgs := getUserOrgs(userId)
	var orgIds []models.Id
	for _, userOrg := range userOrgs {
		orgIds = append(orgIds, userOrg.OrgId)
	}
	if len(orgIds) == 0 {
		return q.Where("1 = 0")
	}

	return q.Where("org_id in (?)", orgIds)
}

func PolicyEnable(tx *db.Session, policyId models.Id, enabled bool) (*models.Policy, e.Error) {
	policy, err := GetPolicyById(tx, policyId)
	if err != nil {
		return nil, err
	}
	policy.Enabled = enabled
	_, er := tx.Save(policy)
	if er != nil {
		return nil, e.New(e.DBError, fmt.Errorf("save policy enable error, id %s", policyId))
	}

	return policy, nil
}

type ScanStatusGroupBy struct {
	Id       models.Id
	Name     string
	Status   string
	Count    int
	Severity string
}

// GetPolicyStatusByPolicy 查询指定时间范围内所有策略的执行结果，统计各策略每种检测状态下的数量
func GetPolicyStatusByPolicy(query *db.Session, from time.Time, to time.Time, status string) ([]*ScanStatusGroupBy, e.Error) {
	groupQuery := query.Model(models.PolicyResult{})
	groupQuery = groupQuery.Where("start_at >= ? and start_at < ?", from, to).
		Select("count(*) as count, policy_id as id, status").
		Group("policy_id,status").
		Order("count desc")

	q := query.Select("r.*,iac_policy.name,iac_policy.severity").Table("(?) as r", groupQuery.Expr()).
		Joins("left join iac_policy on iac_policy.id = r.id")

	if status != "" {
		q = q.Where("r.status = ?", status)
	}
	return findScanStatusGroupBy(q)
}

// GetPolicyStatusByPolicyGroup 查询指定时间范围内所有策略组的执行结果，统计各策略组每种检测状态下的数量
func GetPolicyStatusByPolicyGroup(query *db.Session, from time.Time, to time.Time, status string) ([]*ScanStatusGroupBy, e.Error) {
	groupQuery := query.Model(models.PolicyResult{})
	groupQuery = groupQuery.Where("start_at >= ? and start_at < ?", from, to).
		Select("count(*) as count, policy_group_id as id, status").
		Group("policy_group_id,status").
		Order("count desc")

	q := query.Select("r.*,iac_policy_group.name").Table("(?) as r", groupQuery.Expr()).
		Joins("left join iac_policy_group on iac_policy_group.id = r.id")

	if status != "" {
		q = q.Where("r.status = ?", status)
	}
	return findScanStatusGroupBy(q)
}

func findScanStatusGroupBy(query *db.Session) ([]*ScanStatusGroupBy, e.Error) {
	scanStatus := make([]*ScanStatusGroupBy, 0)
	if err := query.Find(&scanStatus); err != nil {
		return nil, e.New(e.DBError, err)
	}
	return scanStatus, nil
}

// QueryPolicyStatusEveryTargetLastRun 获取指定时间范围内每个策略在任意环境或云模板下的最后一次检测的状态统计
func QueryPolicyStatusEveryTargetLastRun(sess *db.Session, from time.Time, to time.Time) ([]*models.Policy, e.Error) {
	lastScanQuery := sess.Model(models.PolicyResult{}).
		Select("max(id)").
		Group("env_id,tpl_id").
		Where("start_at >= ? AND start_at < ?", from, to)
	lastTaskQuery := sess.Model(models.PolicyResult{}).
		Select("task_id").
		Where("id in (?)", lastScanQuery.Expr())

	// 最后一次检测所有检测结果
	policyLastResultQuery := sess.Model(models.PolicyResult{}).
		Select("id").
		Where("iac_policy_result.task_id in (?)", lastTaskQuery.Expr())

	// 获取策略执行结果的统计数据
	policyResultQuery := sess.Table("iac_policy_result").
		Where("id IN (?)", policyLastResultQuery.Expr()).
		Where("iac_policy_result.status = ? OR iac_policy_result.status = ?",
			common.PolicyStatusFailed, common.PolicyStatusViolated).
		Select("policy_id").
		Group("policy_id")

	// 组合 iac_policy 表，获取策略严重级别
	policyQuery := sess.Table("iac_policy").
		Select("iac_policy.id, iac_policy.severity").
		Joins("join (?) as r on r.policy_id = iac_policy.id", policyResultQuery.Expr())

	policies := make([]*models.Policy, 0)
	if err := policyQuery.Find(&policies); err != nil {
		return nil, e.New(e.DBError, err)
	}

	return policies, nil
}

// PolicyTargetSuppressSummary 获取策略环境/云模板策略屏蔽统计
func PolicyTargetSuppressSummary(query *db.Session, ids []models.Id, scope string) ([]*PolicyScanSummary, e.Error) {
	var key string
	switch scope {
	case consts.ScopeEnv:
		key = "env_id"
	case consts.ScopeTemplate:
		key = "tpl_id"
	}
	// 按来源屏蔽记录
	suppressBySourceQuery := query.Model(models.PolicySuppress{}).
		Select("policy_id, target_id, target_type").
		Where("iac_policy_suppress.target_type= ? and iac_policy_suppress.target_id in (?)", scope, ids)
	// 按策略屏蔽记录
	suppressByPolicyQuery := query.Model(models.Policy{}).
		Select(fmt.Sprintf("iac_policy.id as policy_id, iac_policy_rel.%s as target_id, iac_policy_suppress.target_type", key)).
		Joins("JOIN iac_policy_rel on iac_policy_rel.group_id = iac_policy.group_id").
		Joins("JOIN iac_policy_suppress on iac_policy.id = iac_policy_suppress.target_id").
		Where(fmt.Sprintf("iac_policy_rel.%s in (?)", key), ids)
	// 合并屏蔽记录
	distinctQuery := query.Table("((?) union (?)) as st", suppressBySourceQuery.Expr(), suppressByPolicyQuery.Expr()).
		Select("DISTINCT policy_id, target_id")

	q := query.Table("(?) as s", distinctQuery.Expr()).
		Select("count(*) as count, target_id as id, 'suppressed' as status").
		Group("target_id")

	summary := make([]*PolicyScanSummary, 0)
	if err := q.Find(&summary); err != nil {
		if e.IsRecordNotFound(err) {
			return nil, nil
		}
		return nil, e.New(e.DBError, err)
	}

	return summary, nil
}

func IsTemplateEnabledScan(tx *db.Session, tplId models.Id) (bool, e.Error) {
	if enabled, err := tx.Model(models.PolicyRel{}).Where("tpl_id = ? and group_id = ''", tplId).Exists(); err != nil {
		return false, e.New(e.DBError, err)
	} else {
		return enabled, nil
	}
}

func IsEnvEnabledScan(tx *db.Session, envId models.Id) (bool, e.Error) {
	if enabled, err := tx.Model(models.PolicyRel{}).Where("env_id = ? and group_id = ''", envId).Exists(); err != nil {
		return false, e.New(e.DBError, err)
	} else {
		return enabled, nil
	}
}
