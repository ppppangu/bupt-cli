package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"bupt.local/cli/internal/domain"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

func waitSelected(cmd *cobra.Command) bool {
	value, _ := cmd.Flags().GetBool("wait")
	return value
}

func (a *app) authorizeRestore(cmd *cobra.Command, p map[string]any) error {
	rate, _ := cmd.Flags().GetString("max-points-per-day")
	if !a.dryRun && a.humanOutput() {
		p["dry_run"] = true
		var preview domain.InstanceCreatePreview
		err := a.rpc(cmd.Context(), "instance.restore", p, &preview)
		delete(p, "dry_run")
		if err != nil {
			return err
		}
		if preview.Run != nil && preview.Run.Noop {
			return nil
		}
		if preview.PointsPerDay == "" {
			return domain.E("COST_ESTIMATE_UNAVAILABLE", "学校尚未提供此预留的日价")
		}
		fmt.Fprintf(a.options.Err, "重新分配算力的学校估价 %s 积分/天\n%s\n", scalar(preview.PointsPerDay), preview.BillingStart)
		fmt.Fprintln(a.options.Err, "原磁盘保留。分配完成后，用 bupt compute instance start 启动实例。")
		if rate != "" {
			fmt.Fprintln(a.options.Err, "授权日价上限", scalar(rate), "积分/天")
		}
		fmt.Fprint(a.options.Err, "确认重新分配算力？[y/N] ")
		answer, err := bufio.NewReader(a.options.In).ReadString('\n')
		if err != nil || !strings.EqualFold(strings.TrimSpace(answer), "y") {
			return domain.E("INTERRUPTED", "已取消分配算力")
		}
		if rate == "" {
			rate = preview.PointsPerDay
		}
	}
	if rate != "" {
		p["max_points_per_day"] = rate
	}
	return nil
}

func (a *app) createInstanceCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "create NAME", Short: "申请并启动新实例，需要确认学校计费", Args: exactArgs(1)}
	addResourceFlags(cmd)
	addTaskFlags(cmd)
	f := cmd.Flags()
	allocate := f.Bool("allocate-only", false, "仅申请或分配，仍可能产生预留及存储费用")
	rate := f.String("max-points-per-day", "", "授权学校日价上限（积分/天）")
	f.String("node", "", "选择 SSH 节点")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		p, err := a.resourceParams(cmd, true)
		var detail *domain.Error
		input := bufio.NewReader(a.options.In)
		if errors.As(err, &detail) && detail.Code == "LIMIT_REQUIRED" && a.humanOutput() && !a.dryRun {
			fmt.Fprint(a.options.Err, "运行上限（如 2h） ")
			line, err := input.ReadString('\n')
			if err != nil {
				return domain.E("INTERRUPTED", "已取消创建")
			}
			if err := f.Set("max-runtime", strings.TrimSpace(line)); err != nil {
				return err
			}
			p, err = a.resourceParams(cmd, true)
			if err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		p["name"], p["allocate_only"] = args[0], *allocate
		if !a.dryRun && a.humanOutput() {
			var preview domain.InstanceCreatePreview
			p["dry_run"] = true
			err := a.rpc(cmd.Context(), "instance.create", p, &preview)
			delete(p, "dry_run")
			if err != nil {
				return err
			}
			if !preview.DryRun || preview.PointsPerDay == "" || preview.Run == nil || preview.Run.Deadline.IsZero() {
				return domain.E("COST_ESTIMATE_UNAVAILABLE", "学校尚未返回可核对的日价")
			}
			mode := "创建并启动"
			if *allocate {
				mode = "仅申请或分配"
			}
			fmt.Fprintf(a.options.Err, "%s %s · 规格 %s · 学校估价 %s 积分/天\n截止 %s · 到期处理 %s\n%s\n", mode, scalar(args[0]), scalar(preview.SpecID), scalar(preview.PointsPerDay), preview.Run.Deadline.Local().Format(time.RFC3339), scalar(preview.Run.Policy.Cleanup), preview.BillingStart)
			if len(preview.ResourceConstraints) > 0 {
				fmt.Fprintln(a.options.Err, "资源", scalar(preview.ResourceConstraints))
			}
			if *rate != "" {
				fmt.Fprintln(a.options.Err, "指定日价上限", scalar(*rate), "积分/天")
			}
			fmt.Fprint(a.options.Err, "确认提交？[y/N] ")
			line, err := input.ReadString('\n')
			if err != nil || !strings.EqualFold(strings.TrimSpace(line), "y") {
				return domain.E("INTERRUPTED", "已取消创建")
			}
			if *rate == "" {
				*rate = preview.PointsPerDay
			}
		}
		if !a.dryRun && *rate == "" {
			return usage("非交互式创建需要 --max-points-per-day RATE")
		}
		if *rate != "" {
			p["max_points_per_day"] = *rate
		}
		return a.submitInstance(cmd, "instance.create", p)
	}
	return cmd
}

// Give the caller a durable retry key even if the submission response is lost.
func (a *app) submissionKey(p map[string]any) string {
	if a.dryRun {
		return ""
	}
	key := a.idempotencyKey
	if key == "" {
		key = uuid.NewString()
		p["idempotency_key"] = key
	}
	return key
}

func submissionError(err error, key string) error {
	var problem *domain.Error
	lost := errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) ||
		errors.As(err, &problem) && (problem.Code == "CONTROLLER_IO" || problem.Code == "PROTOCOL_ERROR")
	if key == "" || !lost {
		return err
	}
	code := "SUBMISSION_UNKNOWN"
	if errors.Is(err, context.Canceled) {
		code = "INTERRUPTED"
	}
	return &domain.Error{Code: code, Message: "请求结果待核对。恢复连接后请用相同参数和请求键重试", IdempotencyKey: key, Retryable: true}
}

func (a *app) submitInstance(cmd *cobra.Command, method string, p map[string]any) error {
	p["wait"] = false
	key := a.submissionKey(p)
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var result map[string]any
	if err := a.rpc(ctx, method, p, &result); err != nil {
		return submissionError(err, key)
	}
	if a.dryRun {
		return a.emit(result)
	}
	id, _ := result["id"].(string)
	if r, _ := result["restart_id"].(string); method == "instance.restart" && r != "" {
		id = r
	}
	if id == "" {
		return domain.E("PROTOCOL_ERROR", "控制器未返回操作 ID")
	}
	if waitSelected(cmd) {
		a.humanHint("操作 " + id + " 已提交，正在等待")
		if err := a.rpc(ctx, "instance.wait", map[string]any{"operation": id}, &result); err != nil {
			var detail *domain.Error
			problem := domain.Error{Code: "OPERATION_PENDING", Message: "等待结束，控制器保留本次操作回执", Retryable: true}
			if errors.As(err, &detail) {
				problem = *detail
			}
			if problem.RunID == "" && problem.TaskID == "" {
				switch method {
				case "instance.create", "instance.start", "instance.restart", "instance.restore":
					problem.RunID = id
				default:
					problem.TaskID = id
				}
			}
			if errors.Is(ctx.Err(), context.Canceled) {
				problem.Code = "INTERRUPTED"
			}
			return &problem
		}
	}
	if !a.json && !a.jsonl && !a.ids {
		state := scalar(result["state"])
		if name := instanceProgressLabels[state]; name != "" {
			state = name
		}
		if result["noop"] == true {
			fmt.Fprintln(a.options.Out, "实例已达到请求状态")
		} else if waitSelected(cmd) {
			fmt.Fprintln(a.options.Out, "操作已完成")
		} else if method == "instance.create" {
			fmt.Fprintln(a.options.Out, "已提交创建请求")
		} else {
			fmt.Fprintln(a.options.Out, "操作已提交")
		}
		fmt.Fprintf(a.options.Out, "操作 ID\t%s\n状态\t%s\n", scalar(id), state)
		if name := result["name"]; name != nil {
			fmt.Fprintln(a.options.Out, "实例\t"+scalar(name))
		}
		if method == "instance.create" && !waitSelected(cmd) {
			name, _ := p["name"].(string)
			quoted := shellArg(name)
			if p["allocate_only"] != true {
				a.humanHint("下一步  bupt compute instance wait " + quoted)
			}
			a.humanHint("详情    bupt compute instance show " + quoted)
		}
		return nil
	}
	result["operation_id"] = id
	return a.emit(result)
}

var instanceProgressLabels = map[string]string{
	"preparing": "准备中", "prepared": "已受理", "pending_approval": "等待审批", "queuing": "排队中",
	"reserve_queuing": "等待分配算力", "restoring": "正在分配算力", "starting": "启动中", "stopping": "停机中",
	"ssh_preparing": "正在验证 SSH", "ready": "SSH 已就绪", "allocated": "已分配", "complete": "学校已确认",
}

func shellArg(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func (a *app) waitInstance(cmd *cobra.Command, args []string) error {
	id, _ := cmd.Flags().GetString("operation")
	p := map[string]any{}
	if id != "" {
		if len(args) != 0 || cmd.Flags().Changed("state") || cmd.Flags().Changed("node") {
			return usage("--operation 单独选择回执")
		}
		p["operation"] = id
	} else {
		if len(args) != 1 {
			return usage("提供 INSTANCE 或 --operation ID")
		}
		p["target"] = args[0]
		copyChangedFlags(cmd, p)
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var out json.RawMessage
	if err := a.rpc(ctx, "instance.wait", p, &out); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	return a.emit(out)
}
