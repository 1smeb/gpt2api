package rbac

import "testing"

func TestAdminIncludesPaymentPermissionAndMenu(t *testing.T) {
	if !hasPermission(ListPermissions(RoleAdmin), PermPaymentManage) {
		t.Fatalf("admin permissions missing %s", PermPaymentManage)
	}

	item, ok := findMenu(MenuForRole(RoleAdmin), "admin.payment")
	if !ok {
		t.Fatalf("admin menu missing admin.payment")
	}
	if item.Path != "/admin/payment" {
		t.Fatalf("admin.payment path = %q, want /admin/payment", item.Path)
	}
}

func hasPermission(perms []Permission, want Permission) bool {
	for _, p := range perms {
		if p == want {
			return true
		}
	}
	return false
}

func findMenu(items []Menu, key string) (Menu, bool) {
	for _, item := range items {
		if item.Key == key {
			return item, true
		}
		if found, ok := findMenu(item.Children, key); ok {
			return found, true
		}
	}
	return Menu{}, false
}
