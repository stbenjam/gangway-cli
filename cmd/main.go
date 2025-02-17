package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v2"

	"github.com/openshift-eng/gangway-cli/pkg/api"
)

const strFmt = "%-3s | %-38s | %-80s\n"
const maxJobs = 20

var opts struct {
	initial string
	latest  string
	jobName string
	apiURL  string
	num     int
}

var cmd = &cobra.Command{
	Use:   "mycli",
	Short: "CLI tool to call an API",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Get the MY_APPCI_TOKEN environment variable
		appCIToken := os.Getenv("MY_APPCI_TOKEN")
		if appCIToken == "" {
			cmd.Usage() //nolint
			return fmt.Errorf("cluster token required; please set the MY_APPCI_TOKEN variable")
		}

		// Parse image spec
		spec := api.ImageSpec{
			JobExecutionType: "1",
			PodSpecOptions: api.PodSpecOptions{
				Envs: map[string]string{
					"RELEASE_IMAGE_INITIAL": opts.initial,
					"RELEASE_IMAGE_LATEST":  opts.latest,
				},
			},
		}

		// Convert image spec to JSON and pretty print in case someone needs to
		// debug it.
		data, err := json.MarshalIndent(spec, "", "  ")
		if err != nil {
			return err
		}

		if opts.num > maxJobs {
			return fmt.Errorf("aborting since %d exceeds max value of --n which is %d", opts.num, maxJobs)
		}

		fmt.Println(string(data))

		fmt.Printf(strFmt, "Job", "ID", "URL")
		fmt.Println("---------------------------------------------------------------------------")

		for i := 0; i < opts.num; i++ {
			// Make the HTTP request
			resp, err := launchJob(appCIToken, opts.apiURL, data)
			if err != nil {
				return err
			}

			var jobInfo struct {
				ID string `json:"id"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&jobInfo); err != nil {
				resp.Body.Close() //nolint
				return err
			}
			resp.Body.Close() //nolint

			// Get the job URL from prow easy access
			jobURL, err := getJobURL(jobInfo.ID)
			if err != nil {
				return err
			}

			// Print job info in tabular format
			fmt.Printf(strFmt, strconv.Itoa(i+1), jobInfo.ID, jobURL)

			// Sleep to avoid hitting the api too hard
			time.Sleep(time.Second)
		}

		return nil
	},
}

func NewCommand() *cobra.Command {
	cmd.Flags().StringVarP(&opts.initial, "initial", "i", "", "Initial image")
	cmd.Flags().StringVarP(&opts.latest, "latest", "l", "", "Latest image")
	cmd.Flags().StringVarP(&opts.jobName, "job-name", "j", "", "Job name")
	cmd.Flags().StringVarP(&opts.apiURL, "api-url", "u", "", "API URL")
	cmd.Flags().IntVarP(&opts.num, "n", "n", 1, fmt.Sprintf("Number of times to launch the job (max is %d)", maxJobs))

	return cmd
}

// launchJob launches a prow job using the gangway api authenticated with the token.
func launchJob(appCIToken, apiURL string, data []byte) (*http.Response, error) {
	url := apiURL + "/v1/executions/" + opts.jobName
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(data))
	if err != nil {
		return nil, fmt.Errorf("error creating HTTP request: %v", err)
	}

	req.Header.Set("Authorization", "Bearer "+appCIToken)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error making HTTP request to gangway api: %v", err)
	}

	return resp, nil
}

// getJobURL gets the url from prow so the user has a place to browse to see the
// status of the prow job.  Prow does not immediately have the prow job so we wait.
func getJobURL(jobID string) (string, error) {
	const maxAttempts = 5
	const retryDelay = time.Second
	url := "https://prow.ci.openshift.org/prowjob?prowjob=" + jobID
	var resp *http.Response
	var err error
	for attempts := 0; attempts < maxAttempts; attempts++ {
		resp, err = http.Get(url)
		if err != nil {
			fmt.Printf("Attempt %d: Error getting job URL: %v\n", attempts+1, err)
			time.Sleep(retryDelay)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			resp.Body.Close()
			return "", fmt.Errorf("error reading response body: %v", err)
		}
		resp.Body.Close()

		// Search YAML document parts to find the section with status.url
		documents := strings.Split(string(body), "---")
		var statusURL string
		for _, doc := range documents {
			var jobInfo map[string]interface{}
			if err := yaml.Unmarshal([]byte(doc), &jobInfo); err != nil {
				continue
			}
			if status, ok := jobInfo["status"].(map[interface{}]interface{}); ok {
				if url, ok := status["url"].(string); ok {
					statusURL = url
					return statusURL, nil
				}
			}
		}

		if statusURL == "" {
			time.Sleep(retryDelay)
			continue
		}
		return statusURL, nil
	}
	return "", fmt.Errorf("status.url not found in response after %d retries", maxAttempts)
}
