package main

import (
	"io"
	"os"
	"fmt"
	"log"
	"time"
	"strconv"
	"os/exec"
	"net/http"
	"github.com/unrolled/render"
	"github.com/zenazn/goji/web"
)

func (run *Run) updateStatus(status string) {
	go func() {
		run.Status = status
		run.plan.run_update <- 0
	}()
	<- run.plan.run_update
}

func (run *Run) finished() {
	go func() {
		run.Duration = time.Now().Sub(run.Start)
		run.plan.run_update <- 0
	}()
	<- run.plan.run_update
}
/*
 * Execute the steps for this plan.
 */
func (run *Run) Execute() {

	run.updateStatus("executing")

	for index, step := range run.plan.Steps {
		log.Printf("running step %s\n", step.Name)
		stepPath := fmt.Sprintf("%s/step%d", run.path, index)

		/*
		 * We want to capture the stdout and stderr from each step, so set that up here.
		 */
		stdout, err := os.Create(stepPath + ".out")
		if err != nil {
			run.updateStatus("failure")
			log.Printf("cannot create stdout log for run! out of disk space or inodes?\n")
			break
		}

		stderr, err := os.Create(stepPath + ".err")
		if err != nil {
			run.updateStatus("failure")
			log.Printf("cannot create stderr log for run! out of disk space or inodes?\n")
			break
		}

		/*
		 * Take the payload from this step, turn it into an executable script, and run it.
		 */
		exe, err := os.Create(stepPath)
		if err != nil {
			run.updateStatus("failure")
			log.Printf("cannot create file for payload! out of disk space or inodes?\n")
			break
		}

		exe.WriteString(step.Payload)
		exe.Chmod(0755)
		exe.Close()

		cmd := exec.Command(stepPath)
		cmd.Stdout = stdout
		cmd.Stderr = stderr

		/*
		 * Grab the current environment, and add an env var pointing at the trigger that
		 * caused this run. The body from an HTTP trigger is already dumped into this file,
		 * If the run was triggered by crontab-style execution, the trigger file will just
		 * contain the bare string "scheduled" (without quotes).
		 */
		env := os.Environ()
		env = append(env, fmt.Sprintf("CHANNON_TRIGGER=%s/trigger", run.path))
		cmd.Env = env

		err = cmd.Run()
		stdout.Close()
		stderr.Close()
		if err != nil {
			log.Printf("err was not nil! shit")
			log.Printf(err.Error())
			run.updateStatus("failure")
			break
		}
	}

	run.finished()

	if run.Status != "failure" {
		run.updateStatus("success")
	}

	for _, n := range run.plan.Notifications {
		n := n // the shadow knows. http://golang.org/doc/faq#closures_and_goroutines
		go n.Execute(run)
	}
}

/*
 * This handler will trigger a run from the current plan.
 */
func addRunHandler(pm *PlanManager) func(web.C, http.ResponseWriter, *http.Request) {
	return func(c web.C, w http.ResponseWriter, r *http.Request) {
		planName := c.URLParams["planName"]
		plan := pm.plans[planName]

		path, _ := os.Getwd()
		path = fmt.Sprintf("%s/plans/%s/runs/%d", path, planName, len(plan.Runs))

		// Save the trigger's body before we do anything else
		os.MkdirAll(path, 0755)
		trigger, _ := os.Create(path + "/trigger")
		io.Copy(trigger, r.Body)
		trigger.Close()

		newRunID := uint(len(plan.Runs))
		newRun := Run{Id: newRunID, Status: "pending", Trigger: "post", Start: time.Now(), plan: plan, path: path}

		go func() {
			plan.Runs = append(plan.Runs, &newRun)
			go newRun.Execute()
			plan.run_update <- 0
		}()
		<- plan.run_update

		ren := render.New(render.Options{})
		ren.JSON(w, http.StatusOK, map[string]string{"runID": fmt.Sprintf("%d", newRunID)})
	}
}

/*
 * Get the list of runs for a plan
 */
func listRunsHandler(pm *PlanManager) func(web.C, http.ResponseWriter, *http.Request) {
	return func(c web.C, w http.ResponseWriter, r *http.Request) {
		planName := c.URLParams["planName"]
		plan := pm.plans[planName]

		go func() {
			log.Printf("length of runs list: %d", len(plan.Runs))
			
			ren := render.New(render.Options{})
			ren.JSON(w, http.StatusOK, plan.Runs)
			plan.run_update <- 0
		}()
		<- plan.run_update
	}
}

/*
 * Get the info for a specific run
 */
func getRunHandler(pm *PlanManager) func(web.C, http.ResponseWriter, *http.Request) {
	return func(c web.C, w http.ResponseWriter, r *http.Request) {
		planName := c.URLParams["planName"]
		runID, err := strconv.ParseInt(c.URLParams["runID"], 10, 32)
		if err != nil {
			http.Error(w, err.Error(), 500)
		}

		plan := pm.plans[planName]
		run := plan.Runs[runID]

		ren := render.New(render.Options{})
		ren.JSON(w, http.StatusOK, run)
	}
}

/*
 * Delete a specific run
 */
func deleteRunHandler(pm *PlanManager) func(web.C, http.ResponseWriter, *http.Request) {
	return func(c web.C, w http.ResponseWriter, r *http.Request) {
		planName := c.URLParams["planName"]
		runID, err := strconv.ParseInt(c.URLParams["runID"], 10, 32)
		if err != nil {
			http.Error(w, err.Error(), 500)
		}

		plan := pm.plans[planName]

		go func() {
			plan.Runs[runID] = &Run{}
			plan.run_update <- 0
		}()
		<- plan.run_update
		ren := render.New(render.Options{})
		ren.JSON(w, http.StatusOK, map[string]string{"deleted": fmt.Sprintf("%d", runID)})
	}
}
