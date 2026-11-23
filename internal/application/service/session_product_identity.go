package service

import (
	"context"
	"regexp"
	"strings"

	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
)

const productIdentityAnswer = "我是业问，秒针的企业知识问答助手。"
const productIdentityAnswerEnglish = "I’m Yewen, the enterprise knowledge assistant from Miaozhen."

var (
	selfIdentityQuestion = regexp.MustCompile(`(?i)^(?:(?:请问|请|能不能|能否)\s*)?(?:` +
		`你(?:(?:到底|究竟|实际|又)(?:是)?|是)?(?:谁|什么助手|什么产品|哪个公司的(?:产品)?|` +
		`哪家公司的(?:产品)?|谁做的|谁开发的|哪个公司做的|(?:腾讯|秒针)(?:的)?(?:产品|助手|智能体)?)` +
		`(?:啊|吗|嘛|么|呢)?|` +
		`你(?:叫什么(?:名字)?|是谁(?:开发|做|提供)的)(?:啊|吗|嘛|么|呢)?|` +
		`你的(?:底座|架构|底层|底层架构|产品归属|开发者)(?:是什么|是啥|是谁|是哪个|是哪里)(?:啊|吗|嘛|么|呢)?|` +
		`(?:简单)?介绍一下(?:你自己|自己)|你和(?:腾讯|秒针|业问)(?:是)?(?:什么关系|有什么关系)|` +
		`业问(?:是谁|是什么(?:产品)?|是哪家的(?:产品)?|是谁做的)|` +
		`(?:who|what) are you|what(?:'s| is) your (?:name|product|platform|foundation)|` +
		`who (?:built|made|owns|provides) you|are you (?:a )?(?:tencent|miaozhen)(?: product| assistant)?|` +
		`tell me about yourself)$`)
	upstreamWord             = regexp.MustCompile(`(?i)\bwe[ _-]?knora(?:_[a-z0-9_]+)?\b`)
	upstreamIdentityQuestion = regexp.MustCompile(
		`(?i)关系|底座|底层|技术栈|架构|判断|推断|来源|归属|指的|是不是|不是|你是|是否|来自|基于|开源|介绍|` +
			`说明|是什么|什么意思|什么是|开发|部署|安装|版本|环境变量|` +
			`\b(?:what|who|why|how|describe|explain|introduce|tell me|are you|is it|relationship|based on|` +
			`upstream|foundation|runtime|environment)\b`)
	compoundProductRequest = regexp.MustCompile(
		`(?i)[；;\r\n]|[？?].+|另外|顺便|并且|同时(?:帮|查|回答|比较|总结)|再(?:帮|查|回答|比较|总结)|` +
			`(?:and|also) (?:find|search|show|summarize|compare|answer|retrieve|look up)`)
)

// productIdentityReply handles stand-alone identity and upstream questions before a
// conversation can consult its memory, sources or shell runtime. Mixed requests
// still reach the normal QA path, which carries the same product policy.
func productIdentityReply(ctx context.Context, req *types.QARequest) (string, bool) {
	query := strings.TrimSpace(req.Query)
	if len(query) > 512 || len(req.Attachments)+len(req.ImageURLs)+len(req.SkillNames)+len(req.MCPServiceIDs) > 0 ||
		req.ImageDescription != "" || compoundProductRequest.MatchString(query) {
		return "", false
	}
	query = strings.Join(strings.Fields(strings.Trim(query, "¿¡ \t？?。！!.")), " ")
	upstream := upstreamWord.FindString(query)
	if !selfIdentityQuestion.MatchString(query) &&
		(upstream == "" || !(strings.EqualFold(upstream, query) || upstreamIdentityQuestion.MatchString(query))) {
		return "", false
	}
	if strings.HasPrefix(types.LanguageFromContextOrDefault(ctx), "en") {
		return productIdentityAnswerEnglish, true
	}
	return productIdentityAnswer, true
}

func emitProductIdentityReply(
	ctx context.Context, req *types.QARequest, bus *event.EventBus, answer string, agentMode bool,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := bus.Emit(ctx, event.Event{
		Type: event.EventAgentFinalAnswer, SessionID: req.Session.ID,
		Data: event.AgentFinalAnswerData{Content: answer, Done: true},
	}); err != nil {
		return err
	}
	if !agentMode {
		return nil
	}
	return bus.Emit(context.WithoutCancel(ctx), event.Event{
		Type: event.EventAgentComplete, SessionID: req.Session.ID,
		Data: event.AgentCompleteData{MessageID: req.AssistantMessageID, FinalAnswer: answer},
	})
}
