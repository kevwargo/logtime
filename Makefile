TOPDIR := $(CURDIR)/_rpmbuild

.PHONY: build-rpm install-rpm

build-rpm:
	rm -rf $(TOPDIR)
	mkdir -p $(TOPDIR)/{BUILD,RPMS,SRPMS,SPECS,SOURCES}
	rpmbuild -bb logtime.spec \
		--define "_topdir $(TOPDIR)" \
		--define "_sourcedir $(CURDIR)"
	@echo "RPM built:"
	@find $(TOPDIR)/RPMS -name '*.rpm'

install-rpm:
	sudo dnf install --allow-downgrade $(shell find $(TOPDIR)/RPMS -name '*.rpm')
