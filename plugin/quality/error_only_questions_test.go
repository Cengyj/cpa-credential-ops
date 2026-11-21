package quality

import (
	"context"
	"testing"
)

func TestErrorOnlyQuestionsExecuteWithoutStandardAnswers(t *testing.T) {
	for _, question := range []string{"custom", "visual-pelican-html", "logic-boxes"} {
		for _, scheduled := range []bool{false, true} {
			name := question + "/immediate"
			if scheduled {
				name = question + "/scheduled"
			}
			t.Run(name, func(t *testing.T) {
				f := managementTestFixture(t)
				r := f.rule(t, "observe_only", true, false)
				r.QuestionID, r.Judge.Mode, r.Samples = question, "manual", 2
				if question == "custom" {
					r.Prompt = "Explain the synthetic fixture."
				}
				r.UpstreamErrorActions = map[string]string{"unauthorized": disableCredential}
				var err error
				r, err = f.engine.saveRule(context.Background(), *r, true, "start")
				if err != nil {
					t.Fatal("error-only question rejected", err)
				}
				runs, err := f.round(t, r)
				if err != nil || len(runs) != 1 || len(runs[0].Samples) != 4 || f.probes != 4 || len(f.patches) != 0 {
					t.Fatalf("complete manual-review round: runs=%v err=%v probes=%d writes=%v", runs, err, f.probes, f.patches)
				}
				for _, sample := range runs[0].Samples {
					if sample.ExecutionStatus != "completed" || sample.QualityVerdict != "unknown" {
						t.Fatalf("manual answer became automatic quality evidence: %+v", sample)
					}
				}
				if err := f.engine.Review(runs[0].ID, 0, "incorrect", "synthetic manual review"); err != nil || len(f.patches) != 0 {
					t.Fatal("manual review executed handling", err, f.patches)
				}
				f.upstreamStatus = 401
				var failed *Run
				if scheduled {
					f.engine.Tick(f.engine.rules[r.ID].NextRun)
					task := f.engine.nextRunnableLocked()
					if task == nil || !task.frozen.Scheduled {
						t.Fatal("missing scheduled error-only run")
					}
					f.engine.executeTask(task)
					failed, err = f.engine.Run(task.id)
				} else {
					runs, err = f.round(t, r)
					if len(runs) == 1 {
						failed = runs[0]
					}
				}
				if err != nil || failed == nil || failed.ActionStatus != "credential_suspended" || len(failed.Samples) != 1 ||
					failed.UpstreamCondition != "unauthorized" || f.probes != 5 || len(f.patches) != 1 || !f.patches[0] || f.nativeCalls != 0 {
					t.Fatalf("upstream policy did not act independently: run=%+v err=%v probes=%d writes=%v", failed, err, f.probes, f.patches)
				}
				if failed.Samples[0].QualityVerdict != "unknown" {
					t.Fatal("upstream error counted as wrong answer")
				}
			})
		}
	}
}

func TestAnswerHandlingStillRequiresSupportedAutomaticJudgment(t *testing.T) {
	for _, question := range []string{"custom", "visual-pelican-html", "logic-boxes"} {
		for _, action := range []string{disableCredential, deleteCredential} {
			t.Run(question+"/"+action, func(t *testing.T) {
				f := managementTestFixture(t)
				r := f.rule(t, "observe_only", true, false)
				r.QuestionID, r.Action = question, action
				r.UpstreamErrorActions = map[string]string{"unauthorized": disableCredential}
				if question == "custom" {
					r.Prompt, r.ReviewStandard = "Synthetic prompt", "4"
				}
				if question == "logic-boxes" {
					r.Judge.Mode = "manual"
				}
				if _, err := f.engine.SaveRule(context.Background(), *r, true); err == nil {
					t.Fatal("unsupported answer handling accepted")
				}
				if f.probes != 0 || len(f.patches) != 0 {
					t.Fatal("validation executed a model or mutation")
				}
			})
		}
	}
}
