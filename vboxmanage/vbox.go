package vboxmanage

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"vbox-platform/logger"
)

var vboxPath string

func Init() {
	// Intentar encontrar VBoxManage
	if _, err := exec.LookPath("VBoxManage"); err == nil {
		vboxPath = "VBoxManage"
	} else {
		vboxPath = `C:\Program Files\Oracle\VirtualBox\VBoxManage.exe`
	}
	logger.Get().Info("VBoxManage inicializado en: %s", vboxPath)
}

func Run(args ...string) (string, error) {
	start := time.Now()

	cmd := exec.Command(vboxPath, args...)
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	err := cmd.Run()
	duration := time.Since(start)

	output := out.String()
	if err != nil {
		errOutput := stderr.String()
		logger.Get().LogVBoxCommand(vboxPath, args, duration, errOutput, err)
		return "", fmt.Errorf("%v: %s", err, errOutput)
	}

	logger.Get().LogVBoxCommand(vboxPath, args, duration, output, nil)
	return output, nil
}

func ListVMs() ([]string, error) {
	output, err := Run("list", "vms")
	if err != nil {
		return nil, err
	}

	var vms []string
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for _, line := range lines {
		if line != "" {
			parts := strings.SplitN(line, "\"", 3)
			if len(parts) >= 2 {
				vms = append(vms, parts[1])
			}
		}
	}
	return vms, nil
}

func GetVMInfo(vmName string) (map[string]string, error) {
	output, err := Run("showvminfo", vmName, "--machinereadable")
	if err != nil {
		return nil, err
	}

	info := make(map[string]string)
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.Contains(line, "=") {
			parts := strings.SplitN(line, "=", 2)
			key := strings.Trim(strings.TrimSpace(parts[0]), "\"")
			value := strings.Trim(strings.TrimSpace(parts[1]), "\"")
			info[key] = value
		}
	}
	return info, nil
}

func VMExists(vmName string) (bool, error) {
	vms, err := ListVMs()
	if err != nil {
		return false, err
	}

	for _, vm := range vms {
		if vm == vmName {
			return true, nil
		}
	}
	return false, nil
}

func IsVMRunning(vmName string) (bool, error) {
	info, err := GetVMInfo(vmName)
	if err != nil {
		return false, err
	}

	state := info["VMState"]
	return state == "running", nil
}

func GetDiskType(diskPath string) (string, error) {
	output, err := Run("showmediuminfo", "disk", diskPath)
	if err != nil {
		return "", err
	}

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.Contains(line, "Type:") {
			parts := strings.Split(line, ":")
			if len(parts) >= 2 {
				return strings.TrimSpace(parts[1]), nil
			}
		}
	}
	return "", fmt.Errorf("no se pudo determinar el tipo de disco")
}

func ConvertDiskToMultiAttach(diskPath string) error {
	_, err := Run("modifymedium", "disk", diskPath, "--type", "immutable")
	return err
}

func GetDiskUUID(diskPath string) (string, error) {
	output, err := Run("showmediuminfo", "disk", diskPath)
	if err != nil {
		return "", err
	}

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.Contains(line, "UUID:") {
			parts := strings.Split(line, ":")
			if len(parts) >= 2 {
				uuid := strings.TrimSpace(parts[1])
				return uuid, nil
			}
		}
	}
	return "", fmt.Errorf("no se pudo obtener UUID del disco")
}

func StartVM(vmName string) error {
	_, err := Run("startvm", vmName, "--type", "headless")
	return err
}

func PowerOffVM(vmName string) error {
	_, err := Run("controlvm", vmName, "poweroff")
	return err
}

func GetVMIP(vmName string) (string, error) {
	output, err := Run("guestproperty", "get", vmName, "/VirtualBox/GuestInfo/Net/0/V4/IP")
	if err != nil {
		return "", err
	}

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "Value:") {
			ip := strings.TrimSpace(strings.TrimPrefix(line, "Value:"))
			if ip != "" && ip != "No value set!" {
				return ip, nil
			}
		}
	}
	return "", fmt.Errorf("IP no disponible")
}

func CloneDisk(source, dest string) error {
	_, err := Run("clonemedium", "disk", source, dest, "--variant", "MultiAttach")
	return err
}

func AttachDiskByUUID(vmName, ctrlName, diskUUID string) error {
	_, err := Run("storageattach", vmName, "--storagectl", ctrlName, "--port", "0", "--device", "0", "--type", "hdd", "--medium", diskUUID)
	return err
}

func CreateVM(name, ostype string) error {
	_, err := Run("createvm", "--name", name, "--ostype", ostype, "--register")
	return err
}

func ModifyVM(name string, args ...string) error {
	allArgs := append([]string{"modifyvm", name}, args...)
	_, err := Run(allArgs...)
	return err
}

func AddStorageController(vmName, ctrlName string) error {
	_, err := Run("storagectl", vmName, "--name", ctrlName, "--add", "sata", "--controller", "IntelAhci")
	return err
}

func AttachDisk(vmName, ctrlName, diskPath string) error {
	_, err := Run("storageattach", vmName, "--storagectl", ctrlName, "--port", "0", "--device", "0", "--type", "hdd", "--medium", diskPath)
	return err
}

func DetachDisk(vmName, ctrlName string) error {
	_, err := Run("storageattach", vmName, "--storagectl", ctrlName, "--port", "0", "--device", "0", "--type", "hdd", "--medium", "none")
	return err
}

func UnregisterVM(vmName string) error {
	_, err := Run("unregistervm", vmName, "--delete")
	return err
}

func DeleteDisk(diskPath string) error {
	_, err := Run("closemedium", "disk", diskPath, "--delete")
	return err
}
