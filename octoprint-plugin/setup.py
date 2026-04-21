plugin_identifier = "the_moment"
plugin_package = "octoprint_the_moment"
plugin_name = "The Moment"
plugin_version = "1.0.0"
plugin_description = "Sends print events to The Moment for unified print history and cost tracking."
plugin_author = "The Moment"
plugin_author_email = ""
plugin_url = ""
plugin_license = "GPL-3.0-or-later"

plugin_additional_data = []
plugin_additional_packages = []
plugin_ignored_packages = []

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
