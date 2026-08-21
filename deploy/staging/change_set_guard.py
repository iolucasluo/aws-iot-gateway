#!/usr/bin/env python3

import argparse
import json
from pathlib import Path
from typing import Any


ALLOWED_TASK_DEFINITIONS = {"APITaskDefinition", "WorkerTaskDefinition"}
ALLOWED_ACTIONS = {"Add", "Modify"}
REPLACEMENT_VALUES = {"False", "True", "Conditional"}
ALLOWED_TASK_ROLE_REFERENCES = {"APITaskDefinition": "APITaskRole.Arn"}


def validate_changes(document: dict[str, Any]) -> list[str]:
    errors: list[str] = []
    changes = document.get("Changes")
    if not isinstance(changes, list):
        return ["change set response must contain a Changes array"]
    if not changes:
        return ["change set Changes array must not be empty"]

    non_replacing_roles = {
        resource.get("LogicalResourceId")
        for item in changes
        if isinstance(item, dict)
        and isinstance((resource := item.get("ResourceChange")), dict)
        and resource.get("Action") == "Modify"
        and resource.get("ResourceType") == "AWS::IAM::Role"
        and resource.get("Replacement") == "False"
    }

    for index, item in enumerate(changes):
        resource = item.get("ResourceChange") if isinstance(item, dict) else None
        if not isinstance(resource, dict):
            errors.append(f"change {index} is missing ResourceChange")
            continue

        action = resource.get("Action")
        logical_id = resource.get("LogicalResourceId")
        resource_type = resource.get("ResourceType")
        replacement = resource.get("Replacement")

        if action == "Remove":
            errors.append(f"resource removal is not allowed: {logical_id!r}")
            continue
        if not isinstance(action, str) or action not in ALLOWED_ACTIONS:
            errors.append(f"change {index} has unsupported Action: {action!r}")
            continue
        if not isinstance(logical_id, str) or not logical_id:
            errors.append(f"change {index} has invalid LogicalResourceId")
            continue
        if not isinstance(resource_type, str) or not resource_type:
            errors.append(f"change {index} has invalid ResourceType")
            continue
        if not isinstance(replacement, str) or replacement not in REPLACEMENT_VALUES:
            errors.append(f"change {index} has unsupported Replacement: {replacement!r}")
            continue
        if action == "Add" and replacement != "False":
            errors.append(
                f"new resource must not replace existing state: {logical_id} replacement={replacement}"
            )
            continue
        if replacement == "False":
            continue

        expected_identity = (
            action == "Modify"
            and logical_id in ALLOWED_TASK_DEFINITIONS
            and resource_type == "AWS::ECS::TaskDefinition"
        )
        if not expected_identity:
            errors.append(
                "unexpected replacement: "
                f"{logical_id} action={action} type={resource_type} replacement={replacement}"
            )
            continue

        details = resource.get("Details")
        if not isinstance(details, list) or not details:
            errors.append(f"replacement details are required for {logical_id}")
            continue
        for detail in details:
            target = detail.get("Target") if isinstance(detail, dict) else None
            if not isinstance(target, dict):
                errors.append(f"replacement details are malformed for {logical_id}")
                continue
            container_revision = (
                target.get("Attribute") == "Properties"
                and target.get("Name") == "ContainerDefinitions"
                and target.get("RequiresRecreation") == "Always"
            )
            expected_role_reference = ALLOWED_TASK_ROLE_REFERENCES.get(logical_id)
            role_reference_propagation = (
                expected_role_reference is not None
                and target.get("Attribute") == "Properties"
                and target.get("Name") == "TaskRoleArn"
                and target.get("RequiresRecreation") == "Always"
                and detail.get("Evaluation") == "Dynamic"
                and detail.get("ChangeSource") == "ResourceAttribute"
                and detail.get("CausingEntity") == expected_role_reference
                and expected_role_reference.removesuffix(".Arn") in non_replacing_roles
            )
            if not container_revision and not role_reference_propagation:
                errors.append(
                    f"replacement for {logical_id} must be caused only by "
                    "ContainerDefinitions or an approved non-replacing task-role reference"
                )

    return errors


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("change_set", type=Path)
    args = parser.parse_args()
    document = json.loads(args.change_set.read_text())
    errors = validate_changes(document)
    if errors:
        for error in errors:
            print(error)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
