//go:build integration

package org_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/brianvoe/gofakeit/v6"
	"github.com/muhlemmer/gu"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zitadel/zitadel/internal/integration"
	"github.com/zitadel/zitadel/pkg/grpc/admin"
	v2beta_object "github.com/zitadel/zitadel/pkg/grpc/object/v2beta"
	org "github.com/zitadel/zitadel/pkg/grpc/org/v2beta"
	v2beta_org "github.com/zitadel/zitadel/pkg/grpc/org/v2beta"
	"github.com/zitadel/zitadel/pkg/grpc/user/v2"
	user_v2beta "github.com/zitadel/zitadel/pkg/grpc/user/v2beta"
)

var (
	CTX         context.Context
	Instance    *integration.Instance
	Client      v2beta_org.OrganizationServiceClient
	AdminClient admin.AdminServiceClient
	User        *user.AddHumanUserResponse
)

func TestMain(m *testing.M) {
	os.Exit(func() int {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()

		Instance = integration.NewInstance(ctx)
		Client = Instance.Client.OrgV2beta
		AdminClient = Instance.Client.Admin

		CTX = Instance.WithAuthorization(ctx, integration.UserTypeIAMOwner)
		CTX = Instance.WithAuthorization(ctx, integration.UserTypeIAMOwner)
		User = Instance.CreateHumanUser(CTX)
		return m.Run()
	}())
}

func TestServer_CreateOrganization(t *testing.T) {
	idpResp := Instance.AddGenericOAuthProvider(CTX, Instance.DefaultOrg.Id)

	tests := []struct {
		name    string
		ctx     context.Context
		req     *v2beta_org.CreateOrganizationRequest
		want    *v2beta_org.CreateOrganizationResponse
		wantErr bool
	}{
		{
			name: "missing permission",
			ctx:  Instance.WithAuthorization(context.Background(), integration.UserTypeOrgOwner),
			req: &v2beta_org.CreateOrganizationRequest{
				Name:   "name",
				Admins: nil,
			},
			wantErr: true,
		},
		{
			name: "empty name",
			ctx:  CTX,
			req: &v2beta_org.CreateOrganizationRequest{
				Name:   "",
				Admins: nil,
			},
			wantErr: true,
		},
		{
			name: "invalid admin type",
			ctx:  CTX,
			req: &v2beta_org.CreateOrganizationRequest{
				Name: gofakeit.AppName(),
				Admins: []*v2beta_org.CreateOrganizationRequest_Admin{
					{},
				},
			},
			wantErr: true,
		},
		{
			name: "admin with init",
			ctx:  CTX,
			req: &v2beta_org.CreateOrganizationRequest{
				Name: gofakeit.AppName(),
				Admins: []*v2beta_org.CreateOrganizationRequest_Admin{
					{
						UserType: &v2beta_org.CreateOrganizationRequest_Admin_Human{
							Human: &user_v2beta.AddHumanUserRequest{
								Profile: &user_v2beta.SetHumanProfile{
									GivenName:  "firstname",
									FamilyName: "lastname",
								},
								Email: &user_v2beta.SetHumanEmail{
									Email: gofakeit.Email(),
									Verification: &user_v2beta.SetHumanEmail_ReturnCode{
										ReturnCode: &user_v2beta.ReturnEmailVerificationCode{},
									},
								},
							},
						},
					},
				},
			},
			want: &v2beta_org.CreateOrganizationResponse{
				Id: integration.NotEmpty,
				CreatedAdmins: []*v2beta_org.CreateOrganizationResponse_CreatedAdmin{
					{
						UserId:    integration.NotEmpty,
						EmailCode: gu.Ptr(integration.NotEmpty),
						PhoneCode: nil,
					},
				},
			},
		},
		{
			name: "existing user and new human with idp",
			ctx:  CTX,
			req: &v2beta_org.CreateOrganizationRequest{
				Name: gofakeit.AppName(),
				Admins: []*v2beta_org.CreateOrganizationRequest_Admin{
					{
						UserType: &v2beta_org.CreateOrganizationRequest_Admin_UserId{UserId: User.GetUserId()},
					},
					{
						UserType: &v2beta_org.CreateOrganizationRequest_Admin_Human{
							Human: &user_v2beta.AddHumanUserRequest{
								Profile: &user_v2beta.SetHumanProfile{
									GivenName:  "firstname",
									FamilyName: "lastname",
								},
								Email: &user_v2beta.SetHumanEmail{
									Email: gofakeit.Email(),
									Verification: &user_v2beta.SetHumanEmail_IsVerified{
										IsVerified: true,
									},
								},
								IdpLinks: []*user_v2beta.IDPLink{
									{
										IdpId:    idpResp.Id,
										UserId:   "userID",
										UserName: "username",
									},
								},
							},
						},
					},
				},
			},
			want: &v2beta_org.CreateOrganizationResponse{
				CreatedAdmins: []*v2beta_org.CreateOrganizationResponse_CreatedAdmin{
					// a single admin is expected, because the first provided already exists
					{
						UserId: integration.NotEmpty,
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Client.CreateOrganization(tt.ctx, tt.req)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			// check details
			assert.NotZero(t, got.GetDetails().GetSequence())
			gotCD := got.GetDetails().GetChangeDate().AsTime()
			now := time.Now()
			assert.WithinRange(t, gotCD, now.Add(-time.Minute), now.Add(time.Minute))
			assert.NotEmpty(t, got.GetDetails().GetResourceOwner())

			// organization id must be the same as the resourceOwner
			assert.Equal(t, got.GetDetails().GetResourceOwner(), got.GetId())

			// check the admins
			require.Len(t, got.GetCreatedAdmins(), len(tt.want.GetCreatedAdmins()))
			for i, admin := range tt.want.GetCreatedAdmins() {
				gotAdmin := got.GetCreatedAdmins()[i]
				assertCreatedAdmin(t, admin, gotAdmin)
			}
		})
	}
}

func TestServer_UpdateOrganization(t *testing.T) {
	orgs, orgsName, err := createOrgs(1)
	if err != nil {
		assert.Fail(t, "unable to create org")
	}
	orgId := orgs[0].Id
	orgName := orgsName[0]

	tests := []struct {
		name    string
		ctx     context.Context
		req     *v2beta_org.UpdateOrganizationRequest
		want    *v2beta_org.UpdateOrganizationResponse
		wantErr bool
	}{
		{
			name: "update org with new name",
			ctx:  Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner),
			req: &v2beta_org.UpdateOrganizationRequest{
				Id:   orgId,
				Name: "new org name",
			},
		},
		{
			name: "update org with same name",
			ctx:  Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner),
			req: &v2beta_org.UpdateOrganizationRequest{
				Id:   orgId,
				Name: orgName,
			},
		},
		{
			name: "update org with non existanet org id",
			ctx:  Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner),
			req: &v2beta_org.UpdateOrganizationRequest{
				Id: "non existant org id",
				// Name: "",
			},
			wantErr: true,
		},
		{
			name: "update org with no id",
			ctx:  Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner),
			req: &v2beta_org.UpdateOrganizationRequest{
				Id: orgId,
				// Name: "",
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Client.UpdateOrganization(tt.ctx, tt.req)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			// check details
			assert.NotZero(t, got.GetDetails().GetSequence())
			gotCD := got.GetDetails().GetChangeDate().AsTime()
			now := time.Now()
			assert.WithinRange(t, gotCD, now.Add(-time.Minute), now.Add(time.Minute))
			assert.NotEmpty(t, got.GetDetails().GetResourceOwner())
		})
	}
}

func TestServer_ListOrganization(t *testing.T) {
	noOfOrgs := 3
	orgs, orgsName, err := createOrgs(noOfOrgs)
	if err != nil {
		assert.Fail(t, "unable to create orgs")
	}

	// deactivat org[1]
	_, err = Client.DeactivateOrganization(CTX, &v2beta_org.DeactivateOrganizationRequest{
		Id: orgs[1].Id,
	})
	require.NoError(t, err)

	tests := []struct {
		name    string
		ctx     context.Context
		query   []*v2beta_org.OrgQuery
		want    []*v2beta_org.Organization
		wantErr bool
	}{
		{
			name: "list organizations happy path, no filter",
			ctx:  Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner),
			want: []*v2beta_org.Organization{
				{
					Id:   orgs[0].Id,
					Name: orgsName[0],
				},
				{
					Id:   orgs[1].Id,
					Name: orgsName[1],
				},
				{
					Id:   orgs[2].Id,
					Name: orgsName[2],
				},
			},
		},
		{
			name: "list organizations by id happy path",
			ctx:  Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner),
			query: []*v2beta_org.OrgQuery{
				{
					Query: &v2beta_org.OrgQuery_IdQuery{
						IdQuery: &v2beta_org.OrgIDQuery{
							Id: orgs[1].Id,
						},
					},
				},
			},
			want: []*v2beta_org.Organization{
				{
					Id:   orgs[1].Id,
					Name: orgsName[1],
				},
			},
		},
		{
			name: "list organizations by state active",
			ctx:  Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner),
			query: []*v2beta_org.OrgQuery{
				{
					Query: &v2beta_org.OrgQuery_StateQuery{
						StateQuery: &v2beta_org.OrgStateQuery{
							State: v2beta_org.OrgState_ORG_STATE_ACTIVE,
						},
					},
				},
			},
			want: []*v2beta_org.Organization{
				{
					Id:   orgs[0].Id,
					Name: orgsName[0],
				},
				{
					Id:   orgs[2].Id,
					Name: orgsName[2],
				},
			},
		},
		{
			name: "list organizations by state inactive",
			ctx:  Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner),
			query: []*v2beta_org.OrgQuery{
				{
					Query: &v2beta_org.OrgQuery_StateQuery{
						StateQuery: &v2beta_org.OrgStateQuery{
							State: v2beta_org.OrgState_ORG_STATE_INACTIVE,
						},
					},
				},
			},
			want: []*v2beta_org.Organization{
				{
					Id:   orgs[1].Id,
					Name: orgsName[1],
				},
			},
		},
		{
			name: "list organizations by id bad id",
			ctx:  Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner),
			query: []*v2beta_org.OrgQuery{
				{
					Query: &v2beta_org.OrgQuery_IdQuery{
						IdQuery: &v2beta_org.OrgIDQuery{
							Id: "bad id",
						},
					},
				},
			},
		},
		{
			name: "list organizations specify org name equals",
			ctx:  Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner),
			query: []*v2beta_org.OrgQuery{
				{
					Query: &v2beta_org.OrgQuery_NameQuery{
						NameQuery: &v2beta_org.OrgNameQuery{
							Name:   orgsName[1],
							Method: v2beta_object.TextQueryMethod_TEXT_QUERY_METHOD_EQUALS,
						},
					},
				},
			},
			want: []*v2beta_org.Organization{
				{
					Id:   orgs[1].Id,
					Name: orgsName[1],
				},
			},
		},
		{
			name: "list organizations specify org name contains",
			ctx:  Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner),
			query: []*v2beta_org.OrgQuery{
				{
					Query: &v2beta_org.OrgQuery_NameQuery{
						NameQuery: &v2beta_org.OrgNameQuery{
							Name: func() string {
								return orgsName[1][1 : len(orgsName[1])-2]
							}(),
							Method: v2beta_object.TextQueryMethod_TEXT_QUERY_METHOD_CONTAINS,
						},
					},
				},
			},
			want: []*v2beta_org.Organization{
				{
					Id:   orgs[1].Id,
					Name: orgsName[1],
				},
			},
		},
		{
			name: "list organizations specify org name contains IGNORE CASE",
			ctx:  Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner),
			query: []*v2beta_org.OrgQuery{
				{
					Query: &v2beta_org.OrgQuery_NameQuery{
						NameQuery: &v2beta_org.OrgNameQuery{
							Name: func() string {
								return strings.ToUpper(orgsName[1][1 : len(orgsName[1])-2])
							}(),
							Method: v2beta_object.TextQueryMethod_TEXT_QUERY_METHOD_CONTAINS_IGNORE_CASE,
						},
					},
				},
			},
			want: []*v2beta_org.Organization{
				{
					Id:   orgs[1].Id,
					Name: orgsName[1],
				},
			},
		},
		{
			name: "list organizations specify domain name equals",
			ctx:  Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner),
			query: []*v2beta_org.OrgQuery{
				{
					Query: &org.OrgQuery_DomainQuery{
						DomainQuery: &org.OrgDomainQuery{
							Domain: func() string {
								listOrgRes, err := Client.ListOrganizations(CTX, &v2beta_org.ListOrganizationsRequest{
									Queries: []*v2beta_org.OrgQuery{
										{
											Query: &v2beta_org.OrgQuery_IdQuery{
												IdQuery: &v2beta_org.OrgIDQuery{
													Id: orgs[1].Id,
												},
											},
										},
									},
								})
								require.NoError(t, err)
								domain := listOrgRes.Result[0].PrimaryDomain
								return domain
							}(),
							Method: v2beta_object.TextQueryMethod_TEXT_QUERY_METHOD_EQUALS,
						},
					},
				},
			},
			want: []*v2beta_org.Organization{
				{
					Id:   orgs[1].Id,
					Name: orgsName[1],
				},
			},
		},
		{
			name: "list organizations specify domain name contains",
			ctx:  Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner),
			query: []*v2beta_org.OrgQuery{
				{
					Query: &org.OrgQuery_DomainQuery{
						DomainQuery: &org.OrgDomainQuery{
							Domain: func() string {
								domain := strings.ToLower(strings.ReplaceAll(orgsName[1][1:len(orgsName[1])-2], " ", "-"))
								return domain
							}(),
							Method: v2beta_object.TextQueryMethod_TEXT_QUERY_METHOD_CONTAINS,
						},
					},
				},
			},
			want: []*v2beta_org.Organization{
				{
					Id:   orgs[1].Id,
					Name: orgsName[1],
				},
			},
		},
		{
			name: "list organizations specify org name contains IGNORE CASE",
			ctx:  Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner),
			query: []*v2beta_org.OrgQuery{
				{
					Query: &org.OrgQuery_DomainQuery{
						DomainQuery: &org.OrgDomainQuery{
							Domain: func() string {
								domain := strings.ToUpper(strings.ReplaceAll(orgsName[1][1:len(orgsName[1])-2], " ", "-"))
								return domain
							}(),
							Method: v2beta_object.TextQueryMethod_TEXT_QUERY_METHOD_CONTAINS_IGNORE_CASE,
						},
					},
				},
			},
			want: []*v2beta_org.Organization{
				{
					Id:   orgs[1].Id,
					Name: orgsName[1],
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			retryDuration, tick := integration.WaitForAndTickWithMaxDuration(context.Background(), 10*time.Minute)
			require.EventuallyWithT(t, func(ttt *assert.CollectT) {
				got, err := Client.ListOrganizations(tt.ctx, &v2beta_org.ListOrganizationsRequest{
					Queries: tt.query,
				})

				if tt.wantErr {
					require.Error(t, err)
					return
				}
				require.NoError(t, err)

				foundOrgs := 0
				for _, got := range got.Result {
					for _, org := range tt.want {
						if org.Name == got.Name &&
							org.Id == got.Id {
							foundOrgs += 1
						}
					}
				}
				require.Equal(t, len(tt.want), foundOrgs)
			}, retryDuration, tick, "timeout waiting for expected organizations being created")
		})
	}
}

func TestServer_DeleteOrganization(t *testing.T) {
	tests := []struct {
		name          string
		ctx           context.Context
		createOrgFunc func() string
		req           *v2beta_org.DeleteOrganizationRequest
		want          *v2beta_org.DeleteOrganizationResponse
		err           error
	}{
		{
			name: "delete org happy path",
			ctx:  Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner),
			createOrgFunc: func() string {
				orgs, _, err := createOrgs(1)
				if err != nil {
					assert.Fail(t, "unable to create org")
				}
				return orgs[0].Id
			},
			req: &v2beta_org.DeleteOrganizationRequest{},
		},
		{
			name: "delete non existent org",
			ctx:  Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner),
			req: &v2beta_org.DeleteOrganizationRequest{
				Id: "non existent org id",
			},
			err: fmt.Errorf("Organisation not found"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.createOrgFunc != nil {
				tt.req.Id = tt.createOrgFunc()
			}

			got, err := Client.DeleteOrganization(tt.ctx, tt.req)
			if tt.err != nil {
				require.Contains(t, err.Error(), tt.err.Error())
				return
			}
			require.NoError(t, err)

			// check details
			assert.NotZero(t, got.GetDetails().GetSequence())
			gotCD := got.GetDetails().GetChangeDate().AsTime()
			now := time.Now()
			assert.WithinRange(t, gotCD, now.Add(-time.Minute), now.Add(time.Minute))
			assert.NotEmpty(t, got.GetDetails().GetResourceOwner())

			listOrgRes, err := Client.ListOrganizations(tt.ctx, &v2beta_org.ListOrganizationsRequest{
				Queries: []*v2beta_org.OrgQuery{
					{
						Query: &v2beta_org.OrgQuery_IdQuery{
							IdQuery: &v2beta_org.OrgIDQuery{
								Id: tt.req.Id,
							},
						},
					},
				},
			})
			require.NoError(t, err)
			require.Nil(t, listOrgRes.Result)
		})
	}
}

func TestServer_DeactivateReactivateNonExistentOrganization(t *testing.T) {
	ctx := Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner)

	// deactivate non existent organization
	_, err := Client.DeactivateOrganization(ctx, &v2beta_org.DeactivateOrganizationRequest{
		Id: "non existent organization",
	})
	require.Contains(t, err.Error(), "Organisation not found")

	// reactivate non existent organization
	_, err = Client.ReactivateOrganization(ctx, &v2beta_org.ReactivateOrganizationRequest{
		Id: "non existent organization",
	})
	require.Contains(t, err.Error(), "Organisation not found")
}

func TestServer_DeactivateReactivateOrganization(t *testing.T) {
	// 1. create organization
	orgs, _, err := createOrgs(1)
	if err != nil {
		assert.Fail(t, "unable to create orgs")
	}
	orgId := orgs[0].Id
	ctx := Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner)

	// 2. check inital state of organization
	listOrgRes, err := Client.ListOrganizations(ctx, &v2beta_org.ListOrganizationsRequest{
		Queries: []*v2beta_org.OrgQuery{
			{
				Query: &v2beta_org.OrgQuery_IdQuery{
					IdQuery: &v2beta_org.OrgIDQuery{
						Id: orgId,
					},
				},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, v2beta_org.OrgState_ORG_STATE_ACTIVE, listOrgRes.Result[0].State)

	// 3. deactivate organization once
	deactivate_res, err := Client.DeactivateOrganization(ctx, &v2beta_org.DeactivateOrganizationRequest{
		Id: orgId,
	})
	require.NoError(t, err)
	assert.NotZero(t, deactivate_res.GetDetails().GetSequence())
	gotCD := deactivate_res.GetDetails().GetChangeDate().AsTime()
	now := time.Now()
	assert.WithinRange(t, gotCD, now.Add(-time.Minute), now.Add(time.Minute))
	assert.NotEmpty(t, deactivate_res.GetDetails().GetResourceOwner())

	// 4. check organization state is deactivated
	listOrgRes, err = Client.ListOrganizations(ctx, &v2beta_org.ListOrganizationsRequest{
		Queries: []*v2beta_org.OrgQuery{
			{
				Query: &v2beta_org.OrgQuery_IdQuery{
					IdQuery: &v2beta_org.OrgIDQuery{
						Id: orgId,
					},
				},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, v2beta_org.OrgState_ORG_STATE_INACTIVE, listOrgRes.Result[0].State)

	// 5. repeat deactivate organization once
	// deactivate_res, err = Client.DeactivateOrganization(ctx, &v2beta_org.DeactivateOrganizationRequest{
	_, err = Client.DeactivateOrganization(ctx, &v2beta_org.DeactivateOrganizationRequest{
		Id: orgId,
	})
	// TODO this error message needs to be reoved
	require.Contains(t, err.Error(), "Organisation is already deactivated")

	// 6. repeat check organization state is still deactivated
	listOrgRes, err = Client.ListOrganizations(ctx, &v2beta_org.ListOrganizationsRequest{
		Queries: []*v2beta_org.OrgQuery{
			{
				Query: &v2beta_org.OrgQuery_IdQuery{
					IdQuery: &v2beta_org.OrgIDQuery{
						Id: orgId,
					},
				},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, v2beta_org.OrgState_ORG_STATE_INACTIVE, listOrgRes.Result[0].State)

	// 7. reactivate organization
	reactivate_res, err := Client.ReactivateOrganization(ctx, &v2beta_org.ReactivateOrganizationRequest{
		Id: orgId,
	})
	require.NoError(t, err)
	assert.NotZero(t, reactivate_res.GetDetails().GetSequence())
	gotCD = reactivate_res.GetDetails().GetChangeDate().AsTime()
	now = time.Now()
	assert.WithinRange(t, gotCD, now.Add(-time.Minute), now.Add(time.Minute))
	assert.NotEmpty(t, reactivate_res.GetDetails().GetResourceOwner())

	// 8. check organization state is active
	listOrgRes, err = Client.ListOrganizations(ctx, &v2beta_org.ListOrganizationsRequest{
		Queries: []*v2beta_org.OrgQuery{
			{
				Query: &v2beta_org.OrgQuery_IdQuery{
					IdQuery: &v2beta_org.OrgIDQuery{
						Id: orgId,
					},
				},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, v2beta_org.OrgState_ORG_STATE_ACTIVE, listOrgRes.Result[0].State)

	// 9. repeat reactivate organization
	reactivate_res, err = Client.ReactivateOrganization(ctx, &v2beta_org.ReactivateOrganizationRequest{
		Id: orgId,
	})
	// TODO remove this error message
	require.Contains(t, err.Error(), "Organisation is already active")

	// 10. repeat check organization state is still active
	listOrgRes, err = Client.ListOrganizations(ctx, &v2beta_org.ListOrganizationsRequest{
		Queries: []*v2beta_org.OrgQuery{
			{
				Query: &v2beta_org.OrgQuery_IdQuery{
					IdQuery: &v2beta_org.OrgIDQuery{
						Id: orgId,
					},
				},
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, v2beta_org.OrgState_ORG_STATE_ACTIVE, listOrgRes.Result[0].State)
}

func TestServer_AddOListDeleterganizationDomain(t *testing.T) {
	// 1. create organization
	orgs, _, err := createOrgs(1)
	if err != nil {
		assert.Fail(t, "unable to create org")
	}
	orgId := orgs[0].Id
	ctx := Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner)

	domain := "www.domain.com"
	// 2. add domain
	addOrgDomainRes, err := Client.AddOrganizationDomain(ctx, &v2beta_org.AddOrganizationDomainRequest{
		Id:     orgId,
		Domain: domain,
	})
	require.NoError(t, err)
	// check details
	assert.NotZero(t, addOrgDomainRes.GetDetails().GetSequence())
	gotCD := addOrgDomainRes.GetDetails().GetChangeDate().AsTime()
	now := time.Now()
	assert.WithinRange(t, gotCD, now.Add(-time.Minute), now.Add(time.Minute))
	assert.NotEmpty(t, addOrgDomainRes.GetDetails().GetResourceOwner())

	// 2. check domain is added
	queryRes, err := Client.ListOrganizationDomains(CTX, &v2beta_org.ListOrganizationDomainsRequest{
		Id: orgId,
	})
	require.NoError(t, err)
	found := false
	for _, res := range queryRes.Result {
		if res.DomainName == domain {
			found = true
		}
	}
	require.True(t, found, "unable to find added domain")

	// 3. readd domain
	_, err = Client.AddOrganizationDomain(ctx, &v2beta_org.AddOrganizationDomainRequest{
		Id:     orgId,
		Domain: domain,
	})
	// TODO remove error for adding already existing domain
	// require.NoError(t, err)
	require.Contains(t, err.Error(), "Errors.Already.Exists")
	// check details
	// assert.NotZero(t, addOrgDomainRes.GetDetails().GetSequence())
	// gotCD = addOrgDomainRes.GetDetails().GetChangeDate().AsTime()
	// now = time.Now()
	// assert.WithinRange(t, gotCD, now.Add(-time.Minute), now.Add(time.Minute))
	// assert.NotEmpty(t, addOrgDomainRes.GetDetails().GetResourceOwner())

	// 4. check domain is added
	queryRes, err = Client.ListOrganizationDomains(CTX, &v2beta_org.ListOrganizationDomainsRequest{
		Id: orgId,
	})
	require.NoError(t, err)
	found = false
	for _, res := range queryRes.Result {
		if res.DomainName == domain {
			found = true
		}
	}
	require.True(t, found, "unable to find added domain")

	// 5. delete organisation domain
	deleteOrgDomainRes, err := Client.DeleteOrganizationDomain(ctx, &v2beta_org.DeleteOrganizationDomainRequest{
		Id:     orgId,
		Domain: domain,
	})
	require.NoError(t, err)
	// check details
	assert.NotZero(t, deleteOrgDomainRes.GetDetails().GetSequence())
	gotCD = deleteOrgDomainRes.GetDetails().GetChangeDate().AsTime()
	now = time.Now()
	assert.WithinRange(t, gotCD, now.Add(-time.Minute), now.Add(time.Minute))
	assert.NotEmpty(t, deleteOrgDomainRes.GetDetails().GetResourceOwner())

	// 6. check organization domain deleted
	queryRes, err = Client.ListOrganizationDomains(CTX, &v2beta_org.ListOrganizationDomainsRequest{
		Id: orgId,
	})
	require.NoError(t, err)
	found = false
	for _, res := range queryRes.Result {
		if res.DomainName == domain {
			found = true
		}
	}
	require.False(t, found, "deleted domain found")

	// 7. redelete organisation domain
	_, err = Client.DeleteOrganizationDomain(ctx, &v2beta_org.DeleteOrganizationDomainRequest{
		Id:     orgId,
		Domain: domain,
	})
	// TODO remove error for deleting org domain already deleted
	// require.NoError(t, err)
	require.Contains(t, err.Error(), "Domain doesn't exist on organization")
	// check details
	// assert.NotZero(t, deleteOrgDomainRes.GetDetails().GetSequence())
	// gotCD = deleteOrgDomainRes.GetDetails().GetChangeDate().AsTime()
	// now = time.Now()
	// assert.WithinRange(t, gotCD, now.Add(-time.Minute), now.Add(time.Minute))
	// assert.NotEmpty(t, deleteOrgDomainRes.GetDetails().GetResourceOwner())

	// 8. check organization domain deleted
	queryRes, err = Client.ListOrganizationDomains(CTX, &v2beta_org.ListOrganizationDomainsRequest{
		Id: orgId,
	})
	require.NoError(t, err)
	found = false
	for _, res := range queryRes.Result {
		if res.DomainName == domain {
			found = true
		}
	}
	require.False(t, found, "deleted domain found")
}

func TestServer_ValidateOrganizationDomain(t *testing.T) {
	orgs, _, err := createOrgs(1)
	if err != nil {
		assert.Fail(t, "unable to create org")
	}
	orgId := orgs[0].Id

	_, err = AdminClient.UpdateDomainPolicy(CTX, &admin.UpdateDomainPolicyRequest{
		ValidateOrgDomains: true,
	})
	if err != nil && !strings.Contains(err.Error(), "Organisation is already deactivated") {
		require.NoError(t, err)
	}

	domain := "www.domainnn.com"
	_, err = Client.AddOrganizationDomain(CTX, &v2beta_org.AddOrganizationDomainRequest{
		Id:     orgId,
		Domain: domain,
	})
	require.NoError(t, err)

	tests := []struct {
		name string
		ctx  context.Context
		req  *v2beta_org.GenerateOrganizationDomainValidationRequest
		err  error
	}{
		{
			name: "validate org http happy path",
			ctx:  Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner),
			req: &v2beta_org.GenerateOrganizationDomainValidationRequest{
				Id:     orgId,
				Domain: domain,
				Type:   org.DomainValidationType_DOMAIN_VALIDATION_TYPE_HTTP,
			},
		},
		{
			name: "validate org http non existnetn org id",
			ctx:  Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner),
			req: &v2beta_org.GenerateOrganizationDomainValidationRequest{
				Id:     "non existent org id",
				Domain: domain,
				Type:   org.DomainValidationType_DOMAIN_VALIDATION_TYPE_HTTP,
			},
			// BUG: this should be 'organization does not exist'
			err: errors.New("Domain doesn't exist on organization"),
		},
		{
			name: "validate org dns happy path",
			ctx:  Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner),
			req: &v2beta_org.GenerateOrganizationDomainValidationRequest{
				Id:     orgId,
				Domain: domain,
				Type:   org.DomainValidationType_DOMAIN_VALIDATION_TYPE_DNS,
			},
		},
		{
			name: "validate org dns non existnetn org id",
			ctx:  Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner),
			req: &v2beta_org.GenerateOrganizationDomainValidationRequest{
				Id:     "non existent org id",
				Domain: domain,
				Type:   org.DomainValidationType_DOMAIN_VALIDATION_TYPE_DNS,
			},
			// BUG: this should be 'organization does not exist'
			err: errors.New("Domain doesn't exist on organization"),
		},
		{
			name: "validate org non existnetn domain",
			ctx:  Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner),
			req: &v2beta_org.GenerateOrganizationDomainValidationRequest{
				Id:     orgId,
				Domain: "non existent domain",
				Type:   org.DomainValidationType_DOMAIN_VALIDATION_TYPE_HTTP,
			},
			err: errors.New("Domain doesn't exist on organization"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Client.GenerateOrganizationDomainValidation(tt.ctx, tt.req)
			if tt.err != nil {
				require.Contains(t, err.Error(), tt.err.Error())
				return
			}
			require.NoError(t, err)

			require.NotEmpty(t, got.Token)
			require.Contains(t, got.Url, domain)
		})
	}
}

func TestServer_SetOrganizationMetadata(t *testing.T) {
	orgs, _, err := createOrgs(1)
	if err != nil {
		assert.Fail(t, "unable to create org")
	}
	orgId := orgs[0].Id

	tests := []struct {
		name      string
		ctx       context.Context
		setupFunc func()
		orgId     string
		key       string
		value     string
		err       error
	}{
		{
			name:  "set org metadata",
			ctx:   Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner),
			orgId: orgId,
			key:   "key1",
			value: "value1",
		},
		{
			name:  "set org metadata on non existant org",
			ctx:   Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner),
			orgId: "non existant orgid",
			key:   "key2",
			value: "value2",
			err:   errors.New("Organisation not found"),
		},
		{
			name: "update org metadata",
			ctx:  Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner),
			setupFunc: func() {
				_, err := Client.SetOrganizationMetadata(CTX, &v2beta_org.SetOrganizationMetadataRequest{
					Id: orgId,
					Metadata: []*v2beta_org.Metadata{
						{
							Key:   "key3",
							Value: []byte("value3"),
						},
					},
				})
				require.NoError(t, err)
			},
			orgId: orgId,
			key:   "key4",
			value: "value4",
		},
		{
			name: "update org metadata with same value",
			ctx:  Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner),
			setupFunc: func() {
				_, err := Client.SetOrganizationMetadata(CTX, &v2beta_org.SetOrganizationMetadataRequest{
					Id: orgId,
					Metadata: []*v2beta_org.Metadata{
						{
							Key:   "key5",
							Value: []byte("value5"),
						},
					},
				})
				require.NoError(t, err)
			},
			orgId: orgId,
			key:   "key5",
			value: "value5",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setupFunc != nil {
				tt.setupFunc()
			}
			got, err := Client.SetOrganizationMetadata(tt.ctx, &v2beta_org.SetOrganizationMetadataRequest{
				Id: tt.orgId,
				Metadata: []*v2beta_org.Metadata{
					{
						Key:   tt.key,
						Value: []byte(tt.value),
					},
				},
			})
			if tt.err != nil {
				require.Contains(t, err.Error(), tt.err.Error())
				return
			}
			require.NoError(t, err)

			// check details
			assert.NotZero(t, got.GetDetails().GetSequence())
			gotCD := got.GetDetails().GetChangeDate().AsTime()
			now := time.Now()
			assert.WithinRange(t, gotCD, now.Add(-time.Minute), now.Add(time.Minute))
			assert.NotEmpty(t, got.GetDetails().GetResourceOwner())

			// check metadata
			listMetadataRes, err := Client.ListOrganizationMetadata(tt.ctx, &v2beta_org.ListOrganizationMetadataRequest{
				Id: orgId,
			})
			require.NoError(t, err)
			foundMetadata := false
			foundMetadataKeyCount := 0
			for _, res := range listMetadataRes.Result {
				if res.Key == tt.key {
					foundMetadataKeyCount += 1
				}
				if res.Key == tt.key &&
					string(res.Value) == tt.value {
					foundMetadata = true
				}
			}
			require.True(t, foundMetadata, "unable to find added metadata")
			require.Equal(t, 1, foundMetadataKeyCount, "same metadata key found multiple times")
		})
	}
}

func TestServer_ListOrganizationMetadata(t *testing.T) {
	orgs, _, err := createOrgs(1)
	if err != nil {
		assert.Fail(t, "unable to create org")
	}
	orgId := orgs[0].Id

	tests := []struct {
		name        string
		ctx         context.Context
		setupFunc   func()
		orgId       string
		keyValuPars []struct {
			key   string
			value string
		}
	}{
		{
			name: "list org metadata happy path",
			ctx:  Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner),
			setupFunc: func() {
				_, err := Client.SetOrganizationMetadata(CTX, &v2beta_org.SetOrganizationMetadataRequest{
					Id: orgId,
					Metadata: []*v2beta_org.Metadata{
						{
							Key:   "key1",
							Value: []byte("value1"),
						},
					},
				})
				require.NoError(t, err)
			},
			orgId: orgId,
			keyValuPars: []struct{ key, value string }{
				{
					key:   "key1",
					value: "value1",
				},
			},
		},
		{
			name: "list multiple org metadata happy path",
			ctx:  Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner),
			setupFunc: func() {
				_, err := Client.SetOrganizationMetadata(CTX, &v2beta_org.SetOrganizationMetadataRequest{
					Id: orgId,
					Metadata: []*v2beta_org.Metadata{
						{
							Key:   "key2",
							Value: []byte("value2"),
						},
						{
							Key:   "key3",
							Value: []byte("value3"),
						},
						{
							Key:   "key4",
							Value: []byte("value4"),
						},
					},
				})
				require.NoError(t, err)
			},
			orgId: orgId,
			keyValuPars: []struct{ key, value string }{
				{
					key:   "key2",
					value: "value2",
				},
				{
					key:   "key3",
					value: "value3",
				},
				{
					key:   "key4",
					value: "value4",
				},
			},
		},
		{
			name:        "list org metadata for non existent org",
			ctx:         Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner),
			orgId:       "non existent orgid",
			keyValuPars: []struct{ key, value string }{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setupFunc != nil {
				tt.setupFunc()
			}
			got, err := Client.ListOrganizationMetadata(tt.ctx, &v2beta_org.ListOrganizationMetadataRequest{
				Id: tt.orgId,
			})
			require.NoError(t, err)

			foundMetadataCount := 0
			for _, kv := range tt.keyValuPars {
				for _, res := range got.Result {
					if res.Key == kv.key &&
						string(res.Value) == kv.value {
						foundMetadataCount += 1
					}
				}
			}
			require.Equal(t, len(tt.keyValuPars), foundMetadataCount)
		})
	}
}

func TestServer_DeleteOrganizationMetadata(t *testing.T) {
	orgs, _, err := createOrgs(1)
	if err != nil {
		assert.Fail(t, "unable to create org")
	}
	orgId := orgs[0].Id

	tests := []struct {
		name             string
		ctx              context.Context
		setupFunc        func()
		orgId            string
		metadataToDelete []struct {
			key   string
			value string
		}
		metadataToRemain []struct {
			key   string
			value string
		}
		err error
	}{
		{
			name: "delete org metadata happy path",
			ctx:  Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner),
			setupFunc: func() {
				_, err := Client.SetOrganizationMetadata(CTX, &v2beta_org.SetOrganizationMetadataRequest{
					Id: orgId,
					Metadata: []*v2beta_org.Metadata{
						{
							Key:   "key1",
							Value: []byte("value1"),
						},
					},
				})
				require.NoError(t, err)
			},
			orgId: orgId,
			metadataToDelete: []struct{ key, value string }{
				{
					key:   "key1",
					value: "value1",
				},
			},
		},
		{
			name: "delete multiple org metadata happy path",
			ctx:  Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner),
			setupFunc: func() {
				_, err := Client.SetOrganizationMetadata(CTX, &v2beta_org.SetOrganizationMetadataRequest{
					Id: orgId,
					Metadata: []*v2beta_org.Metadata{
						{
							Key:   "key2",
							Value: []byte("value2"),
						},
						{
							Key:   "key3",
							Value: []byte("value3"),
						},
					},
				})
				require.NoError(t, err)
			},
			orgId: orgId,
			metadataToDelete: []struct{ key, value string }{
				{
					key:   "key2",
					value: "value2",
				},
				{
					key:   "key3",
					value: "value3",
				},
			},
		},
		{
			name: "delete some org metadata but not all",
			ctx:  Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner),
			setupFunc: func() {
				_, err := Client.SetOrganizationMetadata(CTX, &v2beta_org.SetOrganizationMetadataRequest{
					Id: orgId,
					Metadata: []*v2beta_org.Metadata{
						{
							Key:   "key4",
							Value: []byte("value4"),
						},
						// key5 should not be deleted
						{
							Key:   "key5",
							Value: []byte("value5"),
						},
						{
							Key:   "key6",
							Value: []byte("value6"),
						},
					},
				})
				require.NoError(t, err)
			},
			orgId: orgId,
			metadataToDelete: []struct{ key, value string }{
				{
					key:   "key4",
					value: "value4",
				},
				{
					key:   "key6",
					value: "value6",
				},
			},
			metadataToRemain: []struct{ key, value string }{
				{
					key:   "key5",
					value: "value5",
				},
			},
		},
		{
			name: "delete org metadata that does not exist",
			ctx:  Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner),
			setupFunc: func() {
				_, err := Client.SetOrganizationMetadata(CTX, &v2beta_org.SetOrganizationMetadataRequest{
					Id: orgId,
					Metadata: []*v2beta_org.Metadata{
						{
							Key:   "key88",
							Value: []byte("value74"),
						},
						{
							Key:   "key5888",
							Value: []byte("value8885"),
						},
					},
				})
				require.NoError(t, err)
			},
			orgId: orgId,
			// TODO: this error message needs to be either removed or changed
			err: errors.New("Metadata list is empty"),
		},
		{
			name: "delete org metadata for org that does not exist",
			ctx:  Instance.WithAuthorization(context.Background(), integration.UserTypeIAMOwner),
			setupFunc: func() {
				_, err := Client.SetOrganizationMetadata(CTX, &v2beta_org.SetOrganizationMetadataRequest{
					Id: orgId,
					Metadata: []*v2beta_org.Metadata{
						{
							Key:   "key88",
							Value: []byte("value74"),
						},
						{
							Key:   "key5888",
							Value: []byte("value8885"),
						},
					},
				})
				require.NoError(t, err)
			},
			orgId: "non existant org id",
			// TODO: this error message needs to be either removed or changed
			err: errors.New("Metadata list is empty"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setupFunc != nil {
				tt.setupFunc()
			}

			// check metadata exists
			listOrgMetadataRes, err := Client.ListOrganizationMetadata(tt.ctx, &v2beta_org.ListOrganizationMetadataRequest{
				Id: tt.orgId,
			})
			require.NoError(t, err)
			foundMetadataCount := 0
			for _, kv := range tt.metadataToDelete {
				for _, res := range listOrgMetadataRes.Result {
					if res.Key == kv.key &&
						string(res.Value) == kv.value {
						foundMetadataCount += 1
					}
				}
			}
			require.Equal(t, len(tt.metadataToDelete), foundMetadataCount)

			keys := make([]string, len(tt.metadataToDelete))
			for i, kvp := range tt.metadataToDelete {
				keys[i] = kvp.key
			}

			// run delete
			_, err = Client.DeleteOrganizationMetadata(tt.ctx, &v2beta_org.DeleteOrganizationMetadataRequest{
				Id:   tt.orgId,
				Keys: keys,
			})
			if tt.err != nil {
				require.Contains(t, err.Error(), tt.err.Error())
				return
			}
			require.NoError(t, err)

			// check metadata was definitely deleted
			listOrgMetadataRes, err = Client.ListOrganizationMetadata(tt.ctx, &v2beta_org.ListOrganizationMetadataRequest{
				Id: tt.orgId,
			})
			require.NoError(t, err)
			foundMetadataCount = 0
			for _, kv := range tt.metadataToDelete {
				for _, res := range listOrgMetadataRes.Result {
					if res.Key == kv.key &&
						string(res.Value) == kv.value {
						foundMetadataCount += 1
					}
				}
			}
			require.Equal(t, foundMetadataCount, 0)

			// check metadata that should not be delted was not deleted
			listOrgMetadataRes, err = Client.ListOrganizationMetadata(tt.ctx, &v2beta_org.ListOrganizationMetadataRequest{
				Id: tt.orgId,
			})
			require.NoError(t, err)
			foundMetadataCount = 0
			for _, kv := range tt.metadataToRemain {
				for _, res := range listOrgMetadataRes.Result {
					if res.Key == kv.key &&
						string(res.Value) == kv.value {
						foundMetadataCount += 1
					}
				}
			}
			require.Equal(t, len(tt.metadataToRemain), foundMetadataCount)
		})
	}
}

func createOrgs(noOfOrgs int) ([]*v2beta_org.CreateOrganizationResponse, []string, error) {
	var err error
	orgs := make([]*v2beta_org.CreateOrganizationResponse, noOfOrgs)
	orgsName := make([]string, noOfOrgs)

	for i := range noOfOrgs {
		orgName := gofakeit.Name()
		orgsName[i] = orgName
		orgs[i], err = Client.CreateOrganization(CTX,
			&v2beta_org.CreateOrganizationRequest{
				Name: orgName,
			},
		)
		if err != nil {
			return nil, nil, err
		}
	}

	return orgs, orgsName, nil
}

func assertCreatedAdmin(t *testing.T, expected, got *v2beta_org.CreateOrganizationResponse_CreatedAdmin) {
	if expected.GetUserId() != "" {
		assert.NotEmpty(t, got.GetUserId())
	} else {
		assert.Empty(t, got.GetUserId())
	}
	if expected.GetEmailCode() != "" {
		assert.NotEmpty(t, got.GetEmailCode())
	} else {
		assert.Empty(t, got.GetEmailCode())
	}
	if expected.GetPhoneCode() != "" {
		assert.NotEmpty(t, got.GetPhoneCode())
	} else {
		assert.Empty(t, got.GetPhoneCode())
	}
}
