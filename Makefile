VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
VM      := comfynkg-dev
VMARCH  := $(shell orb -m $(VM) uname -m 2>/dev/null | sed 's/aarch64/arm64/;s/x86_64/amd64/')

.PHONY: build test lint dev-vm dev-node vm-install e2e

build: ## release binaries for linux amd64 and arm64 + checksums
	for arch in amd64 arm64; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o dist/comfynkg-linux-$$arch ./cmd/comfynkg || exit 1; \
	done
	cd dist && shasum -a 256 comfynkg-linux-amd64 comfynkg-linux-arm64 > SHA256SUMS

test:
	go test ./...

lint:
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)
	go vet ./...
	staticcheck ./...

dev-vm: ## create and provision the Linux test machine
	orb list | grep -q '^$(VM) ' || orb create ubuntu:24.04 $(VM)
	orb -m $(VM) -u root bash $(CURDIR)/scripts/dev-vm.sh

vm-install: ## build for the VM and install it as /usr/local/bin/comfynkg
	CGO_ENABLED=0 GOOS=linux GOARCH=$(VMARCH) go build -trimpath -ldflags "$(LDFLAGS)" -o dist/comfynkg-linux-$(VMARCH) ./cmd/comfynkg
	orb -m $(VM) -u root install -m 755 $(CURDIR)/dist/comfynkg-linux-$(VMARCH) /usr/local/bin/comfynkg

e2e: vm-install ## run the e2e tests inside the VM as root
	CGO_ENABLED=0 GOOS=linux GOARCH=$(VMARCH) go test -c -tags e2e -o dist/e2e.test ./e2e
	orb -m $(VM) -u root bash -c 'cd $(CURDIR)/e2e && $(CURDIR)/dist/e2e.test -test.v -test.timeout 60m $(if $(RUN),-test.run $(RUN))'

dev-node: ## create the sync worker VM, trust the dev VM's key, name it node0 in the dev VM
	orb list | grep -q '^comfynkg-node ' || orb create ubuntu:24.04 comfynkg-node
	orb -m comfynkg-node -u root bash $(CURDIR)/scripts/dev-node.sh
	orb -m $(VM) -u root bash -c 'test -f /home/comfy/.ssh/id_ed25519 || runuser -u comfy -- ssh-keygen -q -t ed25519 -N "" -f /home/comfy/.ssh/id_ed25519'
	orb -m $(VM) -u root cat /home/comfy/.ssh/id_ed25519.pub | orb -m comfynkg-node -u root bash -c 'cat > /home/comfy/.ssh/authorized_keys && chown comfy:comfy /home/comfy/.ssh/authorized_keys && chmod 600 /home/comfy/.ssh/authorized_keys'
	ip=$$(orb -m comfynkg-node bash -c "ip -4 -o addr show eth0 | awk '{split(\$$4,a,\"/\"); print a[1]}'"); \
	orb -m $(VM) -u root bash -c "sed -i '/ node0$$/d' /etc/hosts && echo '$$ip node0' >> /etc/hosts"
	orb -m $(VM) -u root runuser -u comfy -- ssh -o StrictHostKeyChecking=accept-new -o BatchMode=yes comfy@node0 true
