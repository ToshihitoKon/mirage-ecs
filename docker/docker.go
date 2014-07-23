package docker

import (
	"fmt"
	"log"
	"encoding/json"
	"errors"
	"sort"

	"github.com/acidlemon/mirage/reverseproxy"

	"github.com/fsouza/go-dockerclient"
	"github.com/acidlemon/go-dumper"
)

var endpoint = "unix:///var/run/docker.sock"

type Information struct {
	ID        string `json:"id"`
	SubDomain string `json:"subdomain"`
	GitBranch string `json:"branch"`
	Image     string `json:"image"`
	IPAddress string `json:"ipaddress"`
}

func SetEndpoint(e string) {
	endpoint = e
}

func Launch(subdomain string, gitbranch string, image string) error {
	client, err := docker.NewClient(endpoint)
	if err != nil {
		fmt.Println("cannot create docker client")
		return err
	}

	opt := docker.CreateContainerOptions{
		Config: &docker.Config {
			Image: image,
			Env: []string{ fmt.Sprintf("GIT_BRANCH=%s", gitbranch) },
		},
	}

	container, err := client.CreateContainer(opt)
	if err != nil {
		fmt.Println("cannot create container")
		return err
	}

	err = client.StartContainer(container.ID, nil)
	if err != nil {
		fmt.Println("cannot start container")
		return err
	}

	container, err = client.InspectContainer(container.ID)

	ms := NewMirageStorage()
	defer ms.Close()

	info := Information{
		ID: container.ID,
		SubDomain: subdomain,
		GitBranch: gitbranch,
		Image: image,
		IPAddress: container.NetworkSettings.IPAddress,
	}
	var infoData []byte
	infoData, err = json.Marshal(info)

	err = ms.Set(fmt.Sprintf("subdomain:%s", subdomain), infoData)
	if err != nil {
		fmt.Println(err)
		return err
	}

	ms.AddToSubdomainMap(subdomain)
	reverseproxy.AddSubdomain(subdomain, container.NetworkSettings.IPAddress)

	return nil
}

func Terminate(subdomain string) error {
	ms := NewMirageStorage()
	defer ms.Close()
	data, err := ms.Get(fmt.Sprintf("subdomain:%s", subdomain))
	if err != nil {
		if err == ErrNotFound {
			return errors.New(fmt.Sprintf("no such subdomain: %s", subdomain))
		}
		return errors.New(fmt.Sprintf("cannot find subdomain:%s, err:%s", subdomain, err.Error()))
	}
	var info Information
	json.Unmarshal(data, &info)
	dump.Dump(info)
	containerID := string(info.ID)

	client, err := docker.NewClient(endpoint)
	if err != nil {
		errors.New("cannot create docker client")
	}

	err = client.StopContainer(containerID, 10)
	if err != nil {
		return err
	}

	ms.RemoveFromSubdomainMap(subdomain)
	reverseproxy.RemoveSubdomain(subdomain)

	return nil
}

// extends docker.APIContainers for sort pkg
type ContainerSlice []docker.APIContainers
func (c ContainerSlice) Len() int {
	return len(c)
}
func (c ContainerSlice) Less(i, j int) bool {
	return c[i].ID < c[j].ID
}
func (c ContainerSlice) Swap(i, j int) {
	c[i], c[j] = c[j], c[i]
}


func List() ([]Information, error) {
	client, err := docker.NewClient(endpoint)
	if err != nil {
		fmt.Println("cannot create docker client")
		log.Fatal(err)
	}

	ms := NewMirageStorage()
	subdomainMap, err := ms.GetSubdomainMap()
	if err != nil {
		return nil, err
	}

	containers, _ := client.ListContainers(docker.ListContainersOptions{})
	sort.Sort(ContainerSlice(containers))

	result := []Information{}
	dump.Dump(subdomainMap)
	for subdomain, _ := range subdomainMap {
		infoData, err := ms.Get(fmt.Sprintf("subdomain:%s", subdomain))
		if err != nil {
			fmt.Printf("ms.Get failed err=%s\n", err.Error())
			continue
		}

		var info Information
		err = json.Unmarshal(infoData, &info)
		dump.Dump(info)

		index := sort.Search(len(containers), func(i int) bool { return containers[i].ID >= info.ID })

		if index < len(containers) && containers[index].ID == info.ID {
			// found
			result = append(result, info)
		}
	}

	return result, nil
}


func init() {
	infolist, err := List()
	if err != nil {
		fmt.Println("cannot initialize reverse proxy: ", err.Error())
	}

	for _, info := range infolist {
		reverseproxy.AddSubdomain(info.SubDomain, info.IPAddress)
	}
}


