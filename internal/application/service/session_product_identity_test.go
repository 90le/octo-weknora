package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestProductIdentityReply(t *testing.T) {
	ctx := context.WithValue(t.Context(), types.LanguageContextKey, "zh-CN")
	for _, query := range []string{
		"你是谁？",
		"你是腾讯的产品嘛？",
		"你和weknora有什么关系？",
		"WeKnora 不是你的底座嘛？",
		"你是通过什么回答判断“WeKnora”你是这个底座的？",
		"说明一下 `WEKNORA_SKILL_DIR` 是什么？",
		"介绍一下 WeKnora。",
	} {
		reply, matched := productIdentityReply(ctx, &types.QARequest{Query: query})
		require.True(t, matched, query)
		require.Equal(t, productIdentityAnswer, reply)
	}

	for _, req := range []*types.QARequest{
		{Query: "营销产品的最新报价是什么？"},
		{Query: "你是谁？顺便查找最新报价并引用来源。"},
		{Query: "你和 WeKnora 有什么关系？最新报价在哪里？"},
		{Query: "Summarize the invoice in WeKnora.docx"},
		{Query: "你是谁？", ImageURLs: []string{"attachment"}},
		{Query: "你是谁？", SkillNames: []string{"template"}},
	} {
		_, matched := productIdentityReply(ctx, req)
		require.False(t, matched, req.Query)
	}

	english := context.WithValue(t.Context(), types.LanguageContextKey, "en-US")
	reply, matched := productIdentityReply(english, &types.QARequest{Query: "What are you based on, WEKNORA?"})
	require.True(t, matched)
	require.Equal(t, productIdentityAnswerEnglish, reply)
}

func TestProductIdentityAnswersThroughBothQAServices(t *testing.T) {
	ctx := context.WithValue(t.Context(), types.LanguageContextKey, "zh-CN")
	service := &sessionService{}
	req := &types.QARequest{
		Session:            &types.Session{ID: "session"},
		Query:              "WeKnora 不是你的底座嘛？",
		QuotedContext:      "上次回答猜测底座，并引用过一段运行环境信息。",
		CustomAgent:        &types.CustomAgent{},
		AssistantMessageID: "assistant",
		TurnLeaseHeld:      true,
	}
	for _, spec := range []struct {
		name string
		run  func(context.Context, *types.QARequest, *event.EventBus) error
		want []event.EventType
	}{
		{"quick", service.KnowledgeQA, []event.EventType{event.EventAgentFinalAnswer}},
		{"agent", service.AgentQA, []event.EventType{event.EventAgentFinalAnswer, event.EventAgentComplete}},
	} {
		t.Run(spec.name, func(t *testing.T) {
			bus := event.NewEventBus()
			var emitted []event.EventType
			bus.On(event.EventAgentFinalAnswer, func(_ context.Context, evt event.Event) error {
				emitted = append(emitted, evt.Type)
				data := evt.Data.(event.AgentFinalAnswerData)
				require.Equal(t, productIdentityAnswer, data.Content)
				require.True(t, data.Done)
				return nil
			})
			bus.On(event.EventAgentComplete, func(_ context.Context, evt event.Event) error {
				emitted = append(emitted, evt.Type)
				data := evt.Data.(event.AgentCompleteData)
				require.Equal(t, req.AssistantMessageID, data.MessageID)
				return nil
			})
			require.NoError(t, spec.run(ctx, req, bus))
			require.Equal(t, spec.want, emitted)
		})
	}
}
