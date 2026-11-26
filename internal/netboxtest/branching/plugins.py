# Turns on netbox-branching in the test NetBox's "branching" profile
# (../README.md), as the plugin requires (netbox_branching/__init__.py,
# AppConfig.ready, and docs/netbox-docker.md in netbox-branching 1.2.4).
#
# netbox-docker loads /etc/netbox/config/configuration.py first, then every
# other *.py there in name order, and a later file's setting wins
# (/opt/netbox/netbox/netbox/configuration.py in its image). This file
# replaces netbox-docker's empty plugins.py.

import sys

from netbox_branching.utilities import DynamicSchemaDict

# netbox-branching refuses to start unless DATABASES is a DynamicSchemaDict,
# so wrap the one configuration.py built from the DB_* environment variables.
# netbox-docker registers that file under this module name.
DATABASES = DynamicSchemaDict(sys.modules["netbox.configuration.configuration"].DATABASES)

DATABASE_ROUTERS = ["netbox_branching.database.BranchAwareRouter"]

# netbox-branching must come last.
PLUGINS = ["netbox_branching"]
