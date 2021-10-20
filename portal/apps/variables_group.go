package apps

import (
	"cloudiac/portal/consts/e"
	"cloudiac/portal/libs/ctx"
	"cloudiac/portal/libs/page"
	"cloudiac/portal/models"
	"cloudiac/portal/models/forms"
	"cloudiac/portal/services"
	"cloudiac/utils"
)

type CreateVariableGroupForm struct {
	Name              string                    `json:"name" form:"name"`
	Type              string                    `json:"type" form:"type"`
	VarGroupVariables []VarGroupVariablesCreate `json:"varGroupVariables" form:"varGroupVariables" `
}

type VarGroupVariablesCreate struct {
	Name        string `json:"name" form:"name" `
	Value       string `json:"value" form:"value" `
	Sensitive   bool   `json:"sensitive" form:"sensitive" `
	Description string `json:"description" form:"description" `
}

func CreateVariableGroup(c *ctx.ServiceContext, form *forms.CreateVariableGroupForm) (interface{}, e.Error) {
	session := c.DB()

	vb := make([]models.VarGroupVariable, 0)
	for index, v := range form.VarGroupVariables {
		if v.Sensitive {
			value, _ := utils.AesEncrypt(v.Value)
			form.VarGroupVariables[index].Value = value
		}
		vb = append(vb, models.VarGroupVariable{
			Id:          form.VarGroupVariables[index].Id,
			Name:        form.VarGroupVariables[index].Name,
			Value:       form.VarGroupVariables[index].Value,
			Sensitive:   form.VarGroupVariables[index].Sensitive,
			Description: form.VarGroupVariables[index].Description,
		})
	}
	//创建变量组
	vg, err := services.CreateVariableGroup(session, models.VariableGroup{
		Name:      form.Name,
		Type:      form.Type,
		OrgId:     c.OrgId,
		Variables: models.VarGroupVariables(vb),
	})
	if err != nil {
		return nil, err
	}

	return vg, nil
}

func SearchVariableGroup(c *ctx.ServiceContext, form *forms.SearchVariableGroupForm) (interface{}, e.Error) {
	query := services.SearchVariableGroup(c.DB(), c.OrgId, form.Q)
	p := page.New(form.CurrentPage(), form.PageSize(), query)
	resp := make([]models.VariableGroup, 0)
	if err := p.Scan(&resp); err != nil {
		return nil, e.New(e.DBError, err)
	}
	return page.PageResp{
		Total:    p.MustTotal(),
		PageSize: p.Size,
		List:     resp,
	}, nil
}

func UpdateVariableGroup(c *ctx.ServiceContext, form *forms.UpdateVariableGroupForm) (interface{}, e.Error) {
	session := c.DB()
	attrs := models.Attrs{}

	// 修改变量组
	if form.HasKey("name") {
		attrs["name"] = form.Name
	}

	if form.HasKey("type") {
		attrs["type"] = form.Type
	}

	if form.HasKey("varGroupVariables") {
		vb := make([]models.VarGroupVariable, 0)
		for index, v := range form.VarGroupVariables {
			if v.Sensitive {
				value, _ := utils.AesEncrypt(v.Value)
				form.VarGroupVariables[index].Value = value
			}
			vb = append(vb, models.VarGroupVariable{
				Id:          form.VarGroupVariables[index].Id,
				Name:        form.VarGroupVariables[index].Name,
				Value:       form.VarGroupVariables[index].Value,
				Sensitive:   form.VarGroupVariables[index].Sensitive,
				Description: form.VarGroupVariables[index].Description,
			})
		}
		b, _ := models.VarGroupVariables(vb).Value()
		attrs["variables"] = b
	}

	if err := services.UpdateVariableGroup(session, form.Id, attrs); err != nil {
		return nil, err
	}

	return nil, nil
}

func DeleteVariableGroup(c *ctx.ServiceContext, form *forms.DeleteVariableGroupForm) (interface{}, e.Error) {
	session := c.DB()
	if err := services.DeleteVariableGroup(session, form.Id); err != nil {
		return nil, err
	}
	return nil, nil
}

func DetailVariableGroup(c *ctx.ServiceContext, form *forms.DetailVariableGroupForm) (interface{}, e.Error) {
	vg := models.VariableGroup{}
	vgQuery := services.DetailVariableGroup(c.DB(), form.Id, c.OrgId)
	if err := vgQuery.First(&vg); err != nil {
		return nil, e.New(e.DBError, err)
	}

	return vg, nil
}
