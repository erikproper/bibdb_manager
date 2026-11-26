package main

import (
	"errors"
	"testing"
)

func ask(j *TJob, q TQuestion) string {
	j.take(serviceFrame{T: "question", Q: &q})
	j.take(serviceFrame{T: "ask"})
	return j.openQuestion().ID
}

func TestJobAnswers(t *testing.T) {
	j := newJob("test", "web UI")
	var got []TAnswer
	j.setReply(func(a TAnswer) { got = append(got, a) })
	answer := func(a TAnswer) error {
		_, reply, err := j.takeAnswer(a)
		if err == nil {
			reply(a)
		}
		return err
	}
	q := TQuestion{Kind: "choice", Text: "Which?", Options: []TQuestionOption{{Key: "1"}, {Key: "k", Value: "DBLP key"}, {Key: "q"}}}
	first := ask(j, q)
	for _, bad := range []TAnswer{{Question: first, Choice: "7"}, {Question: first}, {Question: first, Text: "1"},
		{Question: first, Choice: "1", Text: "x"}, {Question: first, Choice: "k", Text: "a\nb"}} {
		if err := answer(bad); err == nil || errors.Is(err, errNoSuchQuestion) || !j.isAsking() {
			t.Errorf("%+v: %v; the question must stay open", bad, err)
		}
	}
	if err := answer(TAnswer{Question: first, Choice: "k", Text: "conf/er/X21"}); err != nil {
		t.Errorf("choice with value: %v", err)
	}
	if err := answer(TAnswer{Question: first, Choice: "1"}); !errors.Is(err, errNoSuchQuestion) {
		t.Errorf("a second answer: %v", err)
	}
	second := ask(j, q)
	if second == first {
		t.Error("the same id for another question")
	}
	if err := answer(TAnswer{Question: first, Choice: "1"}); !errors.Is(err, errNoSuchQuestion) {
		t.Errorf("an answer to an earlier question: %v", err)
	}
	if !j.withdraw() || j.withdraw() {
		t.Error("withdraw")
	}
	if err := answer(TAnswer{Question: second, Choice: "1"}); !errors.Is(err, errNoSuchQuestion) {
		t.Errorf("a withdrawn question: %v", err)
	}
	if len(got) != 1 || got[0].line() != "k" || got[0].value() != "conf/er/X21" {
		t.Errorf("replies: %+v", got)
	}
	j.finish(0)
	if s := j.snapshot(true); s["status"].(TStatus).State != jobStopped || findJob(j.id) != j {
		t.Errorf("retained: %v", s)
	}
}

// A command line's job brings its question up; its answer reaches it once.
func TestCommandLineJobAsks(t *testing.T) {
	sio := &TSocketIO{out: actionsSink{}, interactive: true, lines: make(chan string, 16)}
	j := newJob("test", "command line")
	sio.job = j
	asked := 0
	j.onAsk = func() { asked++ }
	j.setReply(sio.answer)
	sio.SendMessage(serviceFrame{T: "question", Q: &TQuestion{Kind: "choice", Options: []TQuestionOption{{Key: "y"}}}})
	sio.RequestInput()
	if asked != 1 || !j.isAsking() {
		t.Fatal("not brought up")
	}
	_, reply, err := j.takeAnswer(TAnswer{Question: j.openQuestion().ID, Choice: "y"})
	if err != nil {
		t.Fatal(err)
	}
	reply(TAnswer{Choice: "y"})
	reply(TAnswer{Choice: "y"})
	if len(sio.lines) != 1 {
		t.Errorf("%d answers reached the job", len(sio.lines))
	}
	sio.endInput()
	sio.asked.Store(true)
	sio.answer(TAnswer{Choice: "y"}) // after the end: nothing, no panic
	sio.endInput()
}

// Only yes/no questions are asked at a terminal.
func TestTerminalQuestion(t *testing.T) {
	yes := newChoiceQuestion(nil, "Restore?", []string{"n", "y", "q"}, "")
	if !yes.yesNo() || (&TQuestion{Kind: "text"}).yesNo() || newChoiceQuestion(nil, "Which?", []string{"1", "q"}, "").yesNo() {
		t.Error("yesNo")
	}
}

func TestAnswerValue(t *testing.T) {
	setAnswerValue("x")
	if v, ok := takeAnswerValue(); !ok || v != "x" {
		t.Error("value not taken")
	}
	if _, ok := takeAnswerValue(); ok {
		t.Error("value taken twice")
	}
}
