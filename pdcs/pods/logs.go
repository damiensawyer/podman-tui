package pods

import (
	"strings"

	"github.com/containers/podman-tui/pdcs/containers"
	"github.com/containers/podman/v5/pkg/domain/entities"
	"github.com/rs/zerolog/log"
)

// Logs returns logs for all containers in a pod.
func Logs(id string, podInfo *entities.ListPodsReport) ([]string, error) {
	log.Debug().Msgf("pdcs: podman pod logs %s", id)

	var allLogs []string

	for _, container := range podInfo.Containers {
		containerLogs, err := containers.Logs(container.Id)
		if err != nil {
			log.Warn().Msgf("failed to get logs for container %s: %v", container.Id, err)
			continue
		}

		header := strings.Repeat("=", 60) + "\n" +
			"CONTAINER: " + container.Id + "\n" +
			strings.Repeat("=", 60) + "\n"

		allLogs = append(allLogs, header)
		allLogs = append(allLogs, containerLogs...)
		allLogs = append(allLogs, "")
	}

	return allLogs, nil
}
