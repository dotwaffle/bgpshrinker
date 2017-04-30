package main

import "reflect"

func diffSets(new, old []nlri) []change {
	var changes []change
	for _, testNew := range new {
		// are we going to add the prefix? yes, unless we find evidence it's already present
		announce := true
		for _, testOld := range old {
			if equal := reflect.DeepEqual(testNew, testOld); equal == true {
				// ah, it's already present, stop searching and mark as such
				announce = false
				break
			}
		}

		// if we didn't find the new prefix in the old set, announce it
		if announce == true {
			changes = append(changes, change{action: nlriAnnounce, nlri: testNew})
		}
	}

	for _, testOld := range old {
		// are we going to remove the prefix? yes, unless we find evidence it's still present
		withdraw := true
		for _, testNew := range new {
			if equal := reflect.DeepEqual(testOld, testNew); equal == true {
				// ah, it's already present, stop searching and mark as such
				withdraw = false
				break
			}
		}

		// if we didn't find the old prefix in the new set, withdraw it
		if withdraw == true {
			changes = append(changes, change{action: nlriWithdraw, nlri: testOld})
		}
	}

	return nil
}
