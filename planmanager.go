package main

import (
	"os"
	"fmt"
	"log"
	"errors"
	"strings"
	"strconv"
	"encoding/json"
	"path/filepath"
)

type PlanManager struct {
	plans map[string]*Plan
	tags []*Tag
	lock chan int
}

func NewPlanManager() *PlanManager {
	pm := PlanManager{}
	pm.plans = make(map[string]*Plan, 0)
	pm.tags = make([]*Tag, 0)
	pm.lock = make(chan int)

	cwd, _ := os.Getwd()
	plansPath := filepath.Join(cwd, "plans")

	filepath.Walk(plansPath, func(path string, f os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if f.Name() == "plan.json" {
			go func() {
				log.Printf("loading plan from: %s", path)
				p := loadPlan(path)
				pm.plans[p.Name] = p
				pm.lock <- 0
			}()
			<- pm.lock
		}

		return nil
	})
	return &pm
}

/*
 * Save the plan to disk
 */
func savePlan(plan *Plan) {
	path, _ := os.Getwd()
	planPath := filepath.Join(path, "plans", plan.Name)

	if err := os.MkdirAll(planPath, 0755); err != nil {
		log.Printf("cannot make directory for plan %s!\n", plan.Name)
		return
	}

	planFile, err := os.Create(filepath.Join(planPath, "plan.json"))
	if err != nil {
		log.Printf("cannot make file for plan %s!\n", plan.Name)
		return
	}

	enc := json.NewEncoder(planFile)
	enc.Encode(plan)
	defer planFile.Close()

	createStepPayloads(plan)
	createNotificationPayloads(plan)
}

/*
 * Loads a plan from the disk
 */
func loadPlan(planPath string) *Plan {
	planFile, err := os.Open(planPath)
	if err != nil {
		log.Printf("cannot open plan file %s!\n", planPath)
		return nil
	}
	defer planFile.Close()

	plan := NewPlan()
	err = json.NewDecoder(planFile).Decode(plan)
	if err != nil {
		log.Printf("error decoding plan!\n")
		return nil
	}

	runsPath := filepath.Join(filepath.Dir(planPath), "runs")
	filepath.Walk(runsPath, func(path string, f os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if f.Name() == "run.json" {
			go func() {
				runStr := filepath.Base(filepath.Dir(path))
				runID, _ := strconv.ParseUint(runStr, 10, 32)
				log.Printf("loading run %d from: %s", runID, path)
				plan.Runs[uint(runID)] = loadRun(path)
				plan.run_update <- 0
			}()
			<- plan.run_update
		}

		return nil
	})

	return plan
}

func loadRun(path string) *Run {
	r := new(Run)
	runFile, err := os.Open(path)
	if err != nil {
		return nil
	}
	json.NewDecoder(runFile).Decode(r)
	return r
}

/*
 * Create the directory and executable files for a plan's steps.
 */
func createStepPayloads(plan *Plan) {
	// Create the path for this plan so we can create the step scripts.
	path, _ := os.Getwd()
	planPath := fmt.Sprintf("%s/plans/%s", path, plan.Name)

	// Write each step's payload to an executable file.
	for index, step := range plan.Steps {
		stepPath := fmt.Sprintf("%s/step%d", planPath, index)
		if err := os.Remove(stepPath); err != nil {
			if !strings.Contains(err.Error(), "no such file or directory") {
				// That's bad
				log.Printf("Problem removing step %d: %s", index, err.Error())
			}
		}

		exe, err := os.Create(stepPath)
		if err != nil {
			log.Printf("cannot create file for payload! out of disk space or inodes?\n")
			break
		}

		exe.WriteString(step.Payload)
		exe.Chmod(0755)
		exe.Close()
	}
}

func createNotificationPayloads(plan *Plan) {
	for index, n := range plan.Notifications {
		path, _ := os.Getwd()
		notificationPath := fmt.Sprintf("%s/plans/%s/notify-%s", path, plan.Name, n.Target)
		if err := os.Remove(notificationPath); err != nil {
			if !strings.Contains(err.Error(), "no such file or directory") {
				// That's bad
				log.Printf("Problem removing notification %d: %s", index, err.Error())
			}
		}
		exe, err := os.Create(notificationPath)
		if err != nil {
			log.Printf("cannot create notification script! out of disk space or inodes?\n")
			log.Printf(err.Error())
			return
		}

		exe.WriteString(n.Payload)
		exe.Chmod(0755)
		exe.Close()
	}
}

func (pm *PlanManager) AddTag(tag *Tag) {
	go func() {
		pm.tags = append(pm.tags, tag)
		pm.lock <- 0
	}()
	<- pm.lock
}

func (pm *PlanManager) DeleteTag(tag *Tag) {
	ti := tagIndex(pm.tags, tag)
	go func() {
		for _, p := range pm.plans {
			ti := tagIndex(p.Tags, tag)
			if ti > -1 {
				p.Tags[ti], p.Tags[len(p.Tags)-1], p.Tags = p.Tags[len(p.Tags)-1], nil, p.Tags[:len(p.Tags)-1]
			}
		}
		pm.tags[ti], pm.tags[len(pm.tags)-1], pm.tags = pm.tags[len(pm.tags)-1], nil, pm.tags[:len(pm.tags)-1]
		pm.lock <- 0
	}()
	<- pm.lock
}

/*
 * Add the plan to the PlanManager, and create
 * all steps from their payloads.
 */
func (pm *PlanManager) AddPlan(plan *Plan) error {
	if _, exists := pm.plans[plan.Name]; exists {
		return errors.New("The plan name is already taken!")
	}

	go func() {
		pm.plans[plan.Name] = plan
		savePlan(plan)
		pm.lock <- 0
	}()
	<- pm.lock

	return nil
}

func (pm *PlanManager) RenamePlan(oldName, newName string) error {
	if _, exists := pm.plans[oldName]; exists != true {
		return errors.New(fmt.Sprintf("This plan (%s) does not exist!", oldName))
	}

	if _, exists := pm.plans[newName]; exists {
		return errors.New(fmt.Sprintf("The plan (%s) already exists!", newName))
	}

	go func() {
		pm.plans[newName] = pm.plans[oldName]
		delete(pm.plans, oldName)
		path, _ := os.Getwd()
		os.Rename(filepath.Join(path, "plans", oldName), filepath.Join(path, "plans", newName))
		pm.lock <- 0
	}()
	<- pm.lock

	return nil
}

func (pm *PlanManager) UpdatePlan(plan *Plan) error {
	if _, exists := pm.plans[plan.Name]; exists != true {
		return errors.New(fmt.Sprintf("This plan (%s) does not exist!", plan.Name))
	}

	go func() {
		pm.plans[plan.Name] = plan
		savePlan(plan)
		pm.lock <- 0
	}()
	<- pm.lock

	return nil
}

func (pm *PlanManager) GetPlans() []*Plan {
	plans := make([]*Plan, len(pm.plans))
	for _, plan := range pm.plans {
		plans = append(plans, plan)
	}
	return plans
}

func (pm *PlanManager) PlansSummarized(tags []string) (psl PlanSummaryList) {
	for _, plan := range pm.plans {
		if plan.Name == "" {
			continue
		}
		if len(tags) == 0 {
			psl.Names = append(psl.Names, plan.Name)
			continue
		}

		includePlan := false
		for _, tag := range tags {
			for _, ptag := range plan.Tags {
				if tag == string(*ptag) {
					includePlan = true
				}
			}
		}

		if includePlan {
			psl.Names = append(psl.Names, plan.Name)
		}
	}
	return psl
}

func (pm *PlanManager) DeletePlan(name string) {
	go func() {
		delete(pm.plans, name)
		pm.lock <- 0
	}()
	<- pm.lock
}
