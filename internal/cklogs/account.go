package cklogs

import (
	"context"
	"strings"
)

var LoginDatasets = []string{"auth_webmail", "auth_pop3", "auth_smtp", "auth_imap"}

func (s *Service) QueryAccountLog(ctx context.Context, in AccountQuery) AccountResult {
	ds := ResolveDataset(in.Dataset)
	if ds == nil {
		return AccountResult{
			Status:      "not_available",
			Provider:    "none",
			Dataset:     in.Dataset,
			Entries:     []AuthEntry{},
			Limitations: []string{"未知 dataset「" + in.Dataset + "」，未执行任何查询（不会回落到其它日志表）。"},
		}
	}
	if ds.Unavailable != "" {
		return AccountResult{
			Status:      "not_available",
			Provider:    "none",
			Dataset:     ds.Key,
			Protocol:    ds.Protocol,
			Entries:     []AuthEntry{},
			Limitations: []string{UnavailableNote(ds.Unavailable)},
		}
	}
	if s == nil || s.Client == nil || !s.Client.Configured() {
		return AccountResult{
			Status:      "not_available",
			Provider:    "none",
			Dataset:     ds.Key,
			Entries:     []AuthEntry{},
			Limitations: []string{"日志平台凭据未配置（CK_LOGS_BASIC_USER/CK_LOGS_BASIC_PASS），未执行任何查询。"},
		}
	}
	account := ExtractAddr(in.Account)
	if account == "" {
		return AccountResult{
			Status:      "not_available",
			Provider:    "none",
			Dataset:     ds.Key,
			Entries:     []AuthEntry{},
			Limitations: []string{"缺少账号主键（account），登录/操作日志无法按人检索，未执行查询。"},
		}
	}
	filters := Filters{
		Account:   account,
		Op:        in.Op,
		ClientIP:  in.ClientIP,
		TimeRange: in.TimeRange,
	}
	failure := func(r QueryResult) AccountResult {
		status := "error"
		if r.ErrorKind == "imap_table_missing" {
			status = "not_available"
		}
		return AccountResult{
			Status:      status,
			Provider:    "log_platform",
			Dataset:     ds.Key,
			Protocol:    ds.Protocol,
			Code:        ckLogsCode(r.ErrorKind),
			Entries:     []AuthEntry{},
			Limitations: []string{r.Message},
		}
	}

	if !in.CountOnly {
		countRes, _ := s.Client.QueryAuth(ctx, filters, QueryOptions{Dataset: ds.Key, CountOnly: true})
		if !countRes.OK {
			return failure(countRes)
		}
		if countRes.Total == 0 {
			return AccountResult{
				Status:      "ok",
				Provider:    "log_platform",
				Dataset:     ds.Key,
				Protocol:    ds.Protocol,
				Total:       ptrInt(0),
				Entries:     []AuthEntry{},
				Limitations: []string{"该时间窗内按给定条件精确命中 0 条：本表无对应记录（非查询失败、非截断）。"},
				CountOnly:   ptrBool(false),
			}
		}
		res, entries := s.Client.QueryAuth(ctx, filters, QueryOptions{Dataset: ds.Key})
		if !res.OK {
			return AccountResult{
				Status:   "ok",
				Provider: "log_platform",
				Dataset:  ds.Key,
				Protocol: ds.Protocol,
				Total:    ptrInt(countRes.Total),
				Entries:  []AuthEntry{},
				Limitations: []string{
					"精确命中 " + itoa(countRes.Total) + " 条，但明细查询未取回（" + res.ErrorKind + ": " + res.Message + "）——已知有记录、逐条内容不可用；不要据此断言「没有记录」。",
				},
				CountOnly: ptrBool(false),
			}
		}
		lim := []string{}
		origLen := len(entries)
		if ds.VerifyAccount {
			var kept []AuthEntry
			for _, e := range entries {
				if e.Account == "" || strings.ToLower(e.Account) == strings.ToLower(account) {
					kept = append(kept, e)
				}
			}
			filteredOut := origLen - len(kept)
			entries = kept
			note := "本表无可查询的完整地址列，查询按用户名段收窄后已按账号精确校验"
			if filteredOut > 0 {
				note += "，剔除 " + itoa(filteredOut) + " 条同名不同域的记录。"
			} else {
				note += "。"
			}
			note += "命中总数 total 为收窄后的原始条数，可能大于校验后的明细数。"
			lim = append(lim, note)
		}
		if res.Total > origLen {
			lim = append(lim, "命中 "+itoa(res.Total)+" 条，仅返回最近 "+itoa(origLen)+" 条（单页上限 "+itoa(HardSize)+"，截断）。")
		}
		if entries == nil {
			entries = []AuthEntry{}
		}
		return AccountResult{
			Status:      "ok",
			Provider:    "log_platform",
			Dataset:     ds.Key,
			Protocol:    ds.Protocol,
			Total:       ptrInt(res.Total),
			Entries:     entries,
			Limitations: lim,
			CountOnly:   ptrBool(false),
		}
	}

	res, entries := s.Client.QueryAuth(ctx, filters, QueryOptions{Dataset: ds.Key, CountOnly: true})
	if !res.OK {
		return failure(res)
	}
	if entries == nil {
		entries = []AuthEntry{}
	}
	return AccountResult{
		Status:      "ok",
		Provider:    "log_platform",
		Dataset:     ds.Key,
		Protocol:    ds.Protocol,
		Total:       ptrInt(res.Total),
		Entries:     entries,
		Limitations: []string{"本次为计数查询（countOnly），只返回精确命中总数、不含明细。"},
		CountOnly:   ptrBool(true),
	}
}

func (s *Service) QueryLogin(ctx context.Context, account, clientIP string, tr TimeRange, countOnly bool) LoginResult {
	results := make([]AccountResult, 0, len(LoginDatasets))
	for _, ds := range LoginDatasets {
		results = append(results, s.QueryAccountLog(ctx, AccountQuery{
			Dataset:   ds,
			Account:   account,
			ClientIP:  clientIP,
			TimeRange: tr,
			CountOnly: countOnly,
		}))
	}
	return LoginResult{Account: account, Results: results}
}
