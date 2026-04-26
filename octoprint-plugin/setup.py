plugin_identifier = "the_moment"
plugin_package = "octoprint_the_moment"
plugin_name = "The Moment"
plugin_description = "Sends print events to The Moment for unified print history and cost tracking."
plugin_author = "The Moment"
plugin_author_email = ""
plugin_url = ""
plugin_license = "GPL-3.0-or-later"

plugin_additional_data = []
plugin_additional_packages = []
plugin_ignored_packages = []

# ---------------------------------------------------------------------------
# Version — single source of truth is __plugin_version__ in __init__.py.
# setup.py reads it from there so there is only one place to bump the number.
# ---------------------------------------------------------------------------
import ast
import os
import re


def _read_plugin_version():
    init_path = os.path.join(os.path.dirname(__file__), plugin_package, "__init__.py")
    with open(init_path, encoding="utf-8") as fh:
        for line in fh:
            m = re.match(r'^__plugin_version__\s*=\s*(.+)$', line.strip())
            if m:
                return ast.literal_eval(m.group(1))
    raise RuntimeError(
        "Could not find __plugin_version__ in {}/{}/__init__.py".format(
            os.path.dirname(__file__), plugin_package
        )
    )


plugin_version = _read_plugin_version()

from setuptools import setup

setup(
    name=plugin_name,
    version=plugin_version,
    description=plugin_description,
    author=plugin_author,
    author_email=plugin_author_email,
    url=plugin_url,
    license=plugin_license,
    packages=[plugin_package],
    package_data={plugin_package: ["templates/tab_the_moment.jinja2", "static/js/*.js"]},
    install_requires=["requests>=2.20.0"],
    entry_points={
        "octoprint.plugin": ["{} = {}".format(plugin_identifier, plugin_package)]
    },
)
