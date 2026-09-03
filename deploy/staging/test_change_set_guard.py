#!/usr/bin/env python3

import importlib.util
import unittest
from pathlib import Path


GUARD_PATH = Path(__file__).with_name("change_set_guard.py")
SPEC = importlib.util.spec_from_file_location("change_set_guard", GUARD_PATH)
assert SPEC is not None and SPEC.loader is not None
GUARD = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(GUARD)


def replacement(logical_id: str, *, name: str = "ContainerDefinitions", value: str = "True") -> dict:
    return {
        "ResourceChange": {
            "Action": "Modify",
            "LogicalResourceId": logical_id,
            "ResourceType": "AWS::ECS::TaskDefinition",
            "Replacement": value,
            "Details": [
                {
                    "Target": {
                        "Attribute": "Properties",
                        "Name": name,
                        "RequiresRecreation": "Always",
                    }
                }
            ],
        }
    }


class ChangeSetGuardTests(unittest.TestCase):
    def test_rejects_empty_change_array(self) -> None:
        self.assertIn("must not be empty", " ".join(GUARD.validate_changes({"Changes": []})))

    def test_accepts_expected_api_and_worker_task_definition_replacements(self) -> None:
        changes = {
            "Changes": [
                replacement("APITaskDefinition"),
                replacement("WorkerTaskDefinition", value="Conditional"),
                {
                    "ResourceChange": {
                        "Action": "Modify",
                        "LogicalResourceId": "APIService",
                        "ResourceType": "AWS::ECS::Service",
                        "Replacement": "False",
                    }
                },
            ]
        }

        self.assertEqual(GUARD.validate_changes(changes), [])

    def test_accepts_new_resource_without_replacement(self) -> None:
        changes = {
            "Changes": [
                {
                    "ResourceChange": {
                        "Action": "Add",
                        "LogicalResourceId": "OTABucket",
                        "ResourceType": "AWS::S3::Bucket",
                    }
                }
            ]
        }

        self.assertEqual(GUARD.validate_changes(changes), [])

    def test_accepts_api_task_role_reference_propagation_without_role_replacement(self) -> None:
        api_task = replacement("APITaskDefinition")
        api_task["ResourceChange"]["Details"].append(
            {
                "Target": {
                    "Attribute": "Properties",
                    "Name": "TaskRoleArn",
                    "RequiresRecreation": "Always",
                },
                "Evaluation": "Dynamic",
                "ChangeSource": "ResourceAttribute",
                "CausingEntity": "APITaskRole.Arn",
            }
        )
        changes = {
            "Changes": [
                api_task,
                {
                    "ResourceChange": {
                        "Action": "Modify",
                        "LogicalResourceId": "APITaskRole",
                        "ResourceType": "AWS::IAM::Role",
                        "Replacement": "False",
                    }
                },
            ]
        }

        self.assertEqual(GUARD.validate_changes(changes), [])

    def test_rejects_task_role_reference_from_unapproved_role(self) -> None:
        api_task = replacement("APITaskDefinition")
        api_task["ResourceChange"]["Details"].append(
            {
                "Target": {
                    "Attribute": "Properties",
                    "Name": "TaskRoleArn",
                    "RequiresRecreation": "Always",
                },
                "Evaluation": "Dynamic",
                "ChangeSource": "ResourceAttribute",
                "CausingEntity": "UnexpectedRole.Arn",
            }
        )

        self.assertIn(
            "approved non-replacing task-role reference",
            " ".join(GUARD.validate_changes({"Changes": [api_task]})),
        )

    def test_rejects_task_role_reference_without_non_replacing_role_change(self) -> None:
        api_task = replacement("APITaskDefinition")
        api_task["ResourceChange"]["Details"].append(
            {
                "Target": {
                    "Attribute": "Properties",
                    "Name": "TaskRoleArn",
                    "RequiresRecreation": "Always",
                },
                "Evaluation": "Dynamic",
                "ChangeSource": "ResourceAttribute",
                "CausingEntity": "APITaskRole.Arn",
            }
        )

        self.assertIn(
            "approved non-replacing task-role reference",
            " ".join(GUARD.validate_changes({"Changes": [api_task]})),
        )

    def test_rejects_resource_removal(self) -> None:
        changes = {
            "Changes": [
                {
                    "ResourceChange": {
                        "Action": "Remove",
                        "LogicalResourceId": "Database",
                        "ResourceType": "AWS::RDS::DBInstance",
                        "Replacement": "False",
                    }
                }
            ]
        }

        self.assertIn("resource removal", " ".join(GUARD.validate_changes(changes)))

    def test_rejects_unexpected_replacement(self) -> None:
        changes = {"Changes": [replacement("Database")]}

        self.assertIn("unexpected replacement", " ".join(GUARD.validate_changes(changes)))

    def test_rejects_expected_task_definition_replacement_for_other_property(self) -> None:
        changes = {"Changes": [replacement("APITaskDefinition", name="TaskRoleArn")]}

        self.assertIn("ContainerDefinitions", " ".join(GUARD.validate_changes(changes)))

    def test_rejects_replacement_without_details(self) -> None:
        change = replacement("APITaskDefinition")
        change["ResourceChange"]["Details"] = []

        self.assertIn("details", " ".join(GUARD.validate_changes({"Changes": [change]})))

    def test_rejects_missing_or_unknown_action(self) -> None:
        for action in (None, "Import", "Dynamic"):
            with self.subTest(action=action):
                change = replacement("APIService")
                if action is None:
                    del change["ResourceChange"]["Action"]
                else:
                    change["ResourceChange"]["Action"] = action
                self.assertIn(
                    "Action",
                    " ".join(GUARD.validate_changes({"Changes": [change]})),
                )

    def test_rejects_missing_or_unknown_replacement(self) -> None:
        for value in (None, "Dynamic", True):
            with self.subTest(value=value):
                change = replacement("APIService")
                if value is None:
                    del change["ResourceChange"]["Replacement"]
                else:
                    change["ResourceChange"]["Replacement"] = value
                self.assertIn(
                    "Replacement",
                    " ".join(GUARD.validate_changes({"Changes": [change]})),
                )

    def test_rejects_missing_resource_identity(self) -> None:
        for field in ("LogicalResourceId", "ResourceType"):
            with self.subTest(field=field):
                change = replacement("APIService")
                del change["ResourceChange"][field]
                self.assertIn(
                    field,
                    " ".join(GUARD.validate_changes({"Changes": [change]})),
                )


if __name__ == "__main__":
    unittest.main()
