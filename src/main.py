import yaml

with open("../config/glance.yml", "r") as f:
    config_file = yaml.safe_load(f)

feeds = config_file["pages"][0]["columns"][1]["widgets"][0]["widgets"]

topics = []
for f in feeds:
    topics.append(f["title"])

user_topic = int(input(f"In what topic would you like to add the new RSS?\n{topics}:\n"))
link = input("Link of the RSS?:\n")
title = input("Title of the RSS?:\n")

rss = {}
rss["url"] = link
rss["title"] = title

for f in feeds:
    if f["title"] == topics[user_topic]:
        f["feeds"].append(rss)
